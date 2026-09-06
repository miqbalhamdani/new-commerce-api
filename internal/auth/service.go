// Package auth handles signing in, keeping a session alive, and signing out.
package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// ErrUnauthenticated is the single failure this package reports outward.
//
// A wrong password, an unknown email, a disabled account, an expired token and
// a stolen one all collapse into it on purpose. Any difference a client can
// observe -- a different code, a different message, a measurably different
// response time -- tells an attacker which emails exist and which are worth
// pursuing.
var ErrUnauthenticated = errors.New("unauthenticated")

// Session is what login and refresh return.
type Session struct {
	AccessToken  string
	ExpiresIn    int
	RefreshToken string // goes into the cookie, never into the response body
	User         SessionUser
	Tenant       SessionTenant
}

type SessionUser struct {
	ID          uuid.UUID
	Name        string
	Role        string
	Permissions []string
}

type SessionTenant struct {
	ID       uuid.UUID
	Name     string
	Timezone string
	Currency string
}

// Service is the auth use cases. Everything it does that touches a tenant's
// rows goes through InTenantTx; the two lookups that cannot are in
// internal/db/auth_lookup.go and are the system's only cross-tenant reads.
type Service struct {
	store  *db.Store
	signer *Signer
	now    func() time.Time
}

func NewService(store *db.Store, signer *Signer) *Service {
	return &Service{store: store, signer: signer, now: time.Now}
}

// Login exchanges an email and password for a session.
func (s *Service) Login(ctx context.Context, email, password string) (Session, error) {
	user, err := s.store.LookupUserForAuth(ctx, email)
	if errors.Is(err, db.ErrNotFound) {
		// Still spend the time an argon2id verification would take. Returning
		// immediately on an unknown email makes "does this address have an
		// account" answerable with a stopwatch.
		_ = VerifyPassword(dummyHash, password)
		return Session{}, ErrUnauthenticated
	}
	if err != nil {
		return Session{}, err
	}

	// A user mid-invitation has no password set; there is nothing to verify
	// against. Same spend, same answer.
	if user.PasswordHash == nil {
		_ = VerifyPassword(dummyHash, password)
		return Session{}, ErrUnauthenticated
	}
	if err := VerifyPassword(*user.PasswordHash, password); err != nil {
		return Session{}, ErrUnauthenticated
	}
	// Checked after the password, not before: an early return here would make
	// a disabled account distinguishable from a wrong one.
	if user.Status != "active" {
		return Session{}, ErrUnauthenticated
	}

	return s.issue(ctx, user.TenantID, user.ID, user.Role, nil)
}

// Refresh rotates a refresh token and issues a new access token.
//
// This is where the acceptance for P1-011 lives: presenting a token that was
// already rotated means it was stolen, and the response is to end every session
// the user has -- not to reject the one request.
func (s *Service) Refresh(ctx context.Context, presented string) (Session, error) {
	stored, err := s.store.LookupRefreshToken(ctx, HashRefreshToken(presented))
	if errors.Is(err, db.ErrNotFound) {
		return Session{}, ErrUnauthenticated
	}
	if err != nil {
		return Session{}, err
	}

	tenantCtx := tenant.NewContext(ctx, stored.TenantID)

	if stored.RevokedAt != nil {
		// A revoked token was either rotated away or signed out, and the two
		// need very different answers. Only a rotation leaves a successor
		// behind, so that is what tells them apart.
		//
		// Without this distinction a stale browser tab replaying its old cookie
		// after a sign-out would look exactly like a theft, and would sign the
		// user out of every other device.
		var superseded bool
		if err := s.store.InTenantTx(tenantCtx, func(tx pgx.Tx) error {
			q := sqlcgen.New(tx)
			var err error
			if superseded, err = q.HasSuccessor(ctx, &stored.ID); err != nil {
				return err
			}
			if !superseded {
				return nil
			}
			// Rotated away, and presented anyway. The legitimate holder would
			// be carrying the replacement, so whoever sent this has an old
			// copy -- which means a copy was taken.
			//
			// Every active token for the user goes, not only this chain: a
			// chain begins at each login, so someone signed in on a phone and a
			// laptop has two, and revoking one would leave the thief's other
			// session alive while claiming they were signed out everywhere.
			// API spec.md 2 asks for both.
			_, err = q.RevokeAllUserTokens(ctx, stored.UserID)
			return err
		}); err != nil {
			return Session{}, fmt.Errorf("handle revoked refresh token: %w", err)
		}
		return Session{}, ErrUnauthenticated
	}

	if s.now().After(stored.ExpiresAt) {
		return Session{}, ErrUnauthenticated
	}

	var role string
	if err := s.store.InTenantTx(tenantCtx, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		// Revoke first. If this affects no rows, another request rotated the
		// same token between the lookup and here, and continuing would leave
		// two live tokens where there should be one.
		n, err := q.RevokeRefreshToken(ctx, stored.ID)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrUnauthenticated
		}
		row, err := q.GetSession(ctx, stored.UserID)
		if err != nil {
			return err
		}
		role = row.Role
		return nil
	}); err != nil {
		if errors.Is(err, ErrUnauthenticated) {
			return Session{}, ErrUnauthenticated
		}
		return Session{}, err
	}

	return s.issue(ctx, stored.TenantID, stored.UserID, role, &stored.ID)
}

// Logout revokes the presented refresh token.
//
// Silent on every failure. There is nothing useful to tell a caller about a
// session that is already gone, and an error here would let someone probe which
// tokens are live.
func (s *Service) Logout(ctx context.Context, presented string) error {
	if presented == "" {
		return nil
	}
	stored, err := s.store.LookupRefreshToken(ctx, HashRefreshToken(presented))
	if err != nil {
		return nil //nolint:nilerr // deliberate: an unknown token is not an error to report
	}
	return s.store.InTenantTx(tenant.NewContext(ctx, stored.TenantID), func(tx pgx.Tx) error {
		_, err := sqlcgen.New(tx).RevokeRefreshToken(ctx, stored.ID)
		return err
	})
}

// issue mints an access token and a fresh refresh token, and records the new
// refresh token as a rotation of rotatedFrom when there is one.
func (s *Service) issue(ctx context.Context, tenantID, userID uuid.UUID, role string, rotatedFrom *uuid.UUID) (Session, error) {
	now := s.now()

	access, err := s.signer.Issue(userID, tenantID, role, now)
	if err != nil {
		return Session{}, err
	}
	refresh, refreshHash, err := NewRefreshToken()
	if err != nil {
		return Session{}, err
	}

	// uuid.NewV7 rather than gen_random_uuid: time-ordered keys keep B-tree
	// inserts append-only. CLAUDE.md.
	tokenID, err := uuid.NewV7()
	if err != nil {
		return Session{}, fmt.Errorf("generate token id: %w", err)
	}

	var out Session
	err = s.store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if err := q.CreateRefreshToken(ctx, sqlcgen.CreateRefreshTokenParams{
			ID:          tokenID,
			TenantID:    tenantID,
			UserID:      userID,
			TokenHash:   refreshHash,
			RotatedFrom: rotatedFrom,
			ExpiresAt:   now.Add(RefreshTokenTTL),
		}); err != nil {
			return err
		}
		if rotatedFrom == nil {
			if err := q.TouchLastLogin(ctx, userID); err != nil {
				return err
			}
		}

		row, err := q.GetSession(ctx, userID)
		if err != nil {
			return err
		}
		out = Session{
			AccessToken:  access,
			ExpiresIn:    int(AccessTokenTTL.Seconds()),
			RefreshToken: refresh,
			User: SessionUser{
				ID:   row.UserID,
				Name: row.UserName,
				Role: row.Role,
				// Empty until P1-012 seeds the five roles and their
				// resource:action grants. The field is in the contract, so it
				// ships as an empty list rather than being omitted.
				Permissions: []string{},
			},
			Tenant: SessionTenant{
				ID:       row.TenantID,
				Name:     row.TenantName,
				Timezone: row.Timezone,
				Currency: row.Currency,
			},
		}
		return nil
	})
	if err != nil {
		return Session{}, err
	}
	return out, nil
}

// dummyHash is a real argon2id hash of a value nobody knows, verified against
// when there is no user, so that the failing path costs the same as the
// succeeding one.
const dummyHash = "$argon2id$v=19$m=65536,t=3,p=4$c29tZXNhbHR2YWx1ZXg$Zm9yY2luZ2NvbnN0YW50dGltZWNvc3Rvbmx5MDA"
