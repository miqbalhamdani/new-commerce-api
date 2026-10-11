// Package customers is a shop's customer accounts on the storefront
// (P1-206, 04-api-spec.md §11.8): register, sign in, refresh, sign out.
// Accounts belong to one shop (BR-092), so everything here runs in the API
// key's tenant and nothing crosses one.
package customers

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

type Customer = sqlcgen.Customer

// Tokens are a signed-in customer's credentials, returned in the body: a
// cookie set by the API's host would be third-party on the shop's website,
// and Safari drops it (BR-093).
type Tokens struct {
	AccessToken  string
	ExpiresIn    int
	RefreshToken string
	Customer     Customer
}

// refreshPrefix marks a customer refresh token, as sf_live_ marks a key.
const refreshPrefix = "rt_"

type Service struct {
	store  *db.Store
	signer *auth.Signer
}

func NewService(store *db.Store, signer *auth.Signer) *Service {
	return &Service{store: store, signer: signer}
}

// Register creates an account with a password and signs it in. An email
// already registered at this shop is 422 on email; at another shop it is
// unrelated (BR-092). Passwords use the staff parameters (BR-022).
func (s *Service) Register(ctx context.Context, email, password, name string, phone *string) (Tokens, error) {
	email, name = normalEmail(email), strings.TrimSpace(name)
	if _, err := mail.ParseAddress(email); err != nil || strings.ContainsAny(email, "<> ") {
		return Tokens{}, fieldError("email", "email is not an email address")
	}
	if len(password) < 8 {
		return Tokens{}, fieldError("password", "password is at least 8 characters")
	}
	if name == "" {
		return Tokens{}, fieldError("name", "name is required")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return Tokens{}, err
	}
	var out Tokens
	err = s.tx(ctx, func(q *sqlcgen.Queries) error {
		c, err := q.CreateCustomer(ctx, sqlcgen.CreateCustomerParams{ID: uuid.Must(uuid.NewV7()), Email: email,
			PasswordHash: &hash, Name: name, Phone: phone})
		if code, _, ok := db.Violation(err); ok && code == "23505" {
			return fieldError("email", "An account with this email already exists. Sign in, or reset the password.")
		}
		if err != nil {
			return err
		}
		out, err = s.signIn(ctx, q, c)
		return err
	})
	return out, err
}

// Login signs in with email and password. No such account, an account with no
// password (Google only) and a wrong password are one answer at one cost
// (BR-021).
func (s *Service) Login(ctx context.Context, email, password string) (Tokens, error) {
	var out Tokens
	err := s.tx(ctx, func(q *sqlcgen.Queries) error {
		c, err := q.CustomerByEmail(ctx, normalEmail(email))
		if errors.Is(err, pgx.ErrNoRows) || err == nil && c.PasswordHash == nil {
			auth.VerifyNothing(password)
			return failedSignIn()
		}
		if err != nil {
			return err
		}
		if auth.VerifyPassword(*c.PasswordHash, password) != nil {
			return failedSignIn()
		}
		out, err = s.signIn(ctx, q, c)
		return err
	})
	return out, err
}

// Refresh rotates a refresh token (BR-093). A token already rotated away is
// a copy someone kept: the session is revoked, and the caller signs in again.
func (s *Service) Refresh(ctx context.Context, presented string) (Tokens, error) {
	hash := auth.HashRefreshToken(presented)
	var (
		out     Tokens
		revoked bool
	)
	err := s.tx(ctx, func(q *sqlcgen.Queries) error {
		sess, err := q.LockCustomerSession(ctx, hash)
		if errors.Is(err, pgx.ErrNoRows) {
			return failedSignIn()
		}
		if err != nil {
			return err
		}
		if sess.RevokedAt != nil || !sess.ExpiresAt.After(time.Now()) {
			return failedSignIn()
		}
		if sess.RefreshTokenHash != hash {
			// Reuse. Commit the revocation, then refuse.
			revoked = true
			return q.RevokeCustomerSession(ctx, sess.ID)
		}
		raw, next, err := newRefreshToken()
		if err != nil {
			return err
		}
		if err := q.RotateCustomerSession(ctx, sqlcgen.RotateCustomerSessionParams{ID: sess.ID, NewHash: next}); err != nil {
			return err
		}
		c, err := q.CustomerByID(ctx, sess.CustomerID)
		if err != nil {
			return err
		}
		out, err = s.tokens(ctx, c, raw)
		return err
	})
	if err == nil && revoked {
		return Tokens{}, failedSignIn()
	}
	return out, err
}

// Logout revokes the session refreshToken belongs to, if it is customerID's.
// A token that is not theirs, or not live, revokes nothing and is not an
// error: signing out is done either way.
func (s *Service) Logout(ctx context.Context, customerID uuid.UUID, refreshToken string) error {
	return s.tx(ctx, func(q *sqlcgen.Queries) error {
		return q.RevokeCustomerSessionByToken(ctx, sqlcgen.RevokeCustomerSessionByTokenParams{
			CustomerID: customerID, TokenHash: auth.HashRefreshToken(refreshToken)})
	})
}

// signIn starts a session for c and returns its tokens.
func (s *Service) signIn(ctx context.Context, q *sqlcgen.Queries, c Customer) (Tokens, error) {
	raw, hash, err := newRefreshToken()
	if err != nil {
		return Tokens{}, err
	}
	if err := q.CreateCustomerSession(ctx, sqlcgen.CreateCustomerSessionParams{
		ID: uuid.Must(uuid.NewV7()), CustomerID: c.ID, RefreshTokenHash: hash}); err != nil {
		return Tokens{}, err
	}
	return s.tokens(ctx, c, raw)
}

func (s *Service) tokens(ctx context.Context, c Customer, refresh string) (Tokens, error) {
	tenantID, _ := tenant.FromContext(ctx)
	access, err := s.signer.IssueCustomer(c.ID, tenantID, time.Now())
	if err != nil {
		return Tokens{}, err
	}
	return Tokens{AccessToken: access, ExpiresIn: int(auth.AccessTokenTTL.Seconds()), RefreshToken: refresh, Customer: c}, nil
}

// newRefreshToken is rt_ plus 256 random bits, and the hash to store.
func newRefreshToken() (raw, hash string, err error) {
	random, _, err := auth.NewRefreshToken()
	if err != nil {
		return "", "", err
	}
	raw = refreshPrefix + random
	return raw, auth.HashRefreshToken(raw), nil
}

// normalEmail is how emails are stored and matched: trimmed, lower case
// (BR-092; the column's CHECK insists on lower case).
func normalEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

func failedSignIn() error {
	return apperrors.Unauthenticated("The email or password is wrong, or the session has ended. Sign in again.")
}

func (s *Service) tx(ctx context.Context, fn func(q *sqlcgen.Queries) error) error {
	return s.store.InTenantTx(ctx, func(tx pgx.Tx) error { return fn(sqlcgen.New(tx)) })
}

func fieldError(field, detail string) error {
	return apperrors.ValidationFailed(detail).WithFields(apperrors.Field{Name: field, Detail: detail})
}
