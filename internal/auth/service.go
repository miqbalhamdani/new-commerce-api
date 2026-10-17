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

// ErrInvalidCode is every way a signup code can fail: wrong, expired, spent,
// or no unconfirmed signup behind the email. One answer, so a code says
// nothing about the email (BR-021, BR-026).
var ErrInvalidCode = errors.New("invalid code")

// ErrNoAccount is a Google sign-in whose verified email has no user. Unlike a
// password failure it may say so: the caller has just proved the email is
// theirs (BR-020).
var ErrNoAccount = errors.New("no account for this google email")

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
			// BR-022 asks for both.
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
				// The client hides actions it does not find here rather than
				// disabling them, so this list is part of the UI contract and
				// not merely informational.
				Permissions: PermissionsFor(row.Role),
			},
			Tenant: SessionTenant{
				ID:       row.TenantID,
				Name:     row.TenantName,
				Timezone: row.Timezone,
			},
		}
		return nil
	})
	if err != nil {
		return Session{}, err
	}
	return out, nil
}

// VerifyNothing spends what verifying a password costs, for a sign-in that
// has no hash to check -- no such account, or one with no password -- so the
// failure cannot be told from a wrong password with a stopwatch (BR-021).
func VerifyNothing(password string) { _ = VerifyPassword(dummyHash, password) }

// dummyHash is a real argon2id hash of a value nobody knows, verified against
// when there is no user, so that the failing path costs the same as the
// succeeding one.
const dummyHash = "$argon2id$v=19$m=65536,t=3,p=4$c29tZXNhbHR2YWx1ZXg$Zm9yY2luZ2NvbnN0YW50dGltZWNvc3Rvbmx5MDA"

// AcceptInvite sets the invited user's password and signs them in (BR-026).
// The token proves which user at which tenant; the user must still be
// invited, so a link stops working once used. Any failure is
// ErrInvalidInvite: the client can only ask for a new invitation.
func (s *Service) AcceptInvite(ctx context.Context, invites *InviteSigner, token, password string) (Session, error) {
	inv, err := invites.Parse(token, s.now())
	if err != nil {
		return Session{}, ErrInvalidInvite
	}
	hash, err := HashPassword(password)
	if err != nil {
		return Session{}, err
	}
	tctx := tenant.NewActorContext(tenant.NewContext(ctx, inv.TenantID), tenant.Actor{UserID: inv.UserID})
	var role string
	err = s.store.InTenantTx(tctx, func(tx pgx.Tx) error {
		u, err := sqlcgen.New(tx).AcceptInvitation(tctx, sqlcgen.AcceptInvitationParams{ID: inv.UserID, PasswordHash: &hash})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidInvite
		}
		if err != nil {
			return err
		}
		role = u.Role
		return db.Audit(tctx, tx, db.AuditEntry{Action: "user.accept_invite", SubjectType: "user",
			SubjectID: u.ID.String(), After: map[string]any{"status": u.Status}})
	})
	if err != nil {
		return Session{}, err
	}
	return s.issue(ctx, inv.TenantID, inv.UserID, role, nil)
}

// ResetPassword sets a staff user's password with a reset token, signs them
// out everywhere and signs them in afresh (P1-088, BR-094). resets must be the
// staff tokens, so a customer's link never gets here. A Google-only user adds a
// password this way. Any failure is ErrInvalidReset: the client can only ask
// for a new link.
func (s *Service) ResetPassword(ctx context.Context, resets *ResetTokens, token, password string) (Session, error) {
	r, err := resets.Parse(token, s.now())
	if err != nil {
		return Session{}, ErrInvalidReset
	}
	hash, err := HashPassword(password)
	if err != nil {
		return Session{}, err
	}
	tctx := tenant.NewActorContext(tenant.NewContext(ctx, r.TenantID), tenant.Actor{UserID: r.ID})
	var role string
	err = s.store.InTenantTx(tctx, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		u, err := q.GetUser(tctx, r.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidReset
		}
		if err != nil {
			return err
		}
		if !resets.Verify(token, u.PasswordHash) {
			return ErrInvalidReset
		}
		// Guarded on the old hash and on active: a link sets a password once,
		// and never for a user disabled since it was sent.
		n, err := q.SetUserPassword(tctx, sqlcgen.SetUserPasswordParams{ID: u.ID, NewHash: &hash, OldHash: u.PasswordHash})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrInvalidReset
		}
		if _, err := q.RevokeAllUserTokens(tctx, u.ID); err != nil {
			return err
		}
		role = u.Role
		return db.Audit(tctx, tx, db.AuditEntry{Action: "user.password_reset", SubjectType: "user", SubjectID: u.ID.String()})
	})
	if err != nil {
		return Session{}, err
	}
	return s.issue(ctx, r.TenantID, r.ID, role, nil)
}

// VerifyCode exchanges a signup's emailed code for the invitation token its
// link carries (BR-026). The code only confirms the address: the session still
// needs a password, through AcceptInvite. The user is found through the
// staff-login lookup (BR-003).
func (s *Service) VerifyCode(ctx context.Context, invites *InviteSigner, email, code string) (string, error) {
	u, err := s.store.LookupUserForAuth(ctx, email)
	if errors.Is(err, db.ErrNotFound) {
		return "", ErrInvalidCode
	}
	if err != nil {
		return "", err
	}
	if u.Status != "invited" || u.PasswordHash != nil || !invites.CheckCode(u.ID, u.TenantID, code, s.now()) {
		return "", ErrInvalidCode
	}
	return invites.Mint(u.ID, u.TenantID, s.now()), nil
}

// GoogleSignIn signs in the user a Google ID token's verified email names
// (BR-020), with the platform's client ID. An invited user has just proved the
// address, so their invitation is accepted first, with no password. A token
// Google does not vouch for, or a disabled user, is ErrUnauthenticated; no
// user is ErrNoAccount.
func (s *Service) GoogleSignIn(ctx context.Context, google *GoogleVerifier, clientID, idToken string) (Session, error) {
	id, err := google.Verify(ctx, idToken, clientID)
	if err != nil {
		return Session{}, ErrUnauthenticated
	}
	u, err := s.store.LookupUserForAuth(ctx, id.Email)
	if errors.Is(err, db.ErrNotFound) {
		return Session{}, ErrNoAccount
	}
	if err != nil {
		return Session{}, err
	}
	switch u.Status {
	case "active":
	case "invited":
		tctx := tenant.NewActorContext(tenant.NewContext(ctx, u.TenantID), tenant.Actor{UserID: u.ID})
		err := s.store.InTenantTx(tctx, func(tx pgx.Tx) error {
			accepted, err := sqlcgen.New(tx).ActivateInvited(tctx, u.ID)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrUnauthenticated // disabled or accepted in between
			}
			if err != nil {
				return err
			}
			return db.Audit(tctx, tx, db.AuditEntry{Action: "user.accept_invite", SubjectType: "user",
				SubjectID: accepted.ID.String(), After: map[string]any{"status": accepted.Status, "via": "google"}})
		})
		if err != nil {
			return Session{}, err
		}
	default:
		return Session{}, ErrUnauthenticated
	}
	return s.issue(ctx, u.TenantID, u.ID, u.Role, nil)
}

// SessionFor signs in a user the caller has just authenticated another way --
// an owner whose Google signup created them (BR-020). Nothing else may call it.
func (s *Service) SessionFor(ctx context.Context, tenantID, userID uuid.UUID, role string) (Session, error) {
	return s.issue(ctx, tenantID, userID, role, nil)
}
