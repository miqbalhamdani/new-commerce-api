package customers

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

// Google sign-in (P1-223, 04-api-spec.md §11.8; BR-092, BR-127).

// GoogleSignIn signs in with a Google ID token for the shop's own client ID,
// and reports whether the customer was made just now. Which customer, in
// order: the one this Google account signed in as before; the account with
// the same Google-verified email, which the login is now linked to; a new
// customer with no password (BR-127). A customer is never made with neither
// a password nor a login.
func (s *Service) GoogleSignIn(ctx context.Context, idToken string) (Tokens, bool, error) {
	var clientID *string
	err := s.tx(ctx, func(q *sqlcgen.Queries) error {
		st, err := q.GetStorefrontSettings(ctx)
		if err == nil {
			clientID = st.GoogleClientID
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return nil
	})
	if err != nil {
		return Tokens{}, false, err
	}
	if clientID == nil {
		return Tokens{}, false, fieldError("id_token", "This shop has not set up Google sign-in.")
	}
	if s.Google == nil {
		return Tokens{}, false, apperrors.Internal(errors.New("no google verifier"))
	}
	id, err := s.Google.Verify(ctx, idToken, *clientID)
	if err != nil {
		return Tokens{}, false, apperrors.Unauthenticated("Google could not confirm this sign-in. Try again.")
	}

	var (
		out     Tokens
		created bool
	)
	// Twice at most: two first sign-ins racing both try to create, and the
	// loser finds the winner's login on the second pass.
	for range 2 {
		created = false
		err = s.tx(ctx, func(q *sqlcgen.Queries) error {
			c, err := q.CustomerByIdentity(ctx, sqlcgen.CustomerByIdentityParams{Provider: "google", Subject: id.Subject})
			if errors.Is(err, pgx.ErrNoRows) {
				c, created, err = s.linkOrCreate(ctx, q, id)
			}
			if err != nil {
				return err
			}
			out, err = s.signIn(ctx, q, c)
			return err
		})
		if code, constraint, ok := db.Violation(err); ok && code == "23505" && constraint == "customer_identities_pkey" {
			continue
		}
		break
	}
	return out, created, err
}

// linkOrCreate links this Google login to the customer with its verified
// email, or makes a new customer with no password.
func (s *Service) linkOrCreate(ctx context.Context, q *sqlcgen.Queries, id auth.GoogleIdentity) (Customer, bool, error) {
	email := normalEmail(id.Email)
	c, err := q.CustomerByEmail(ctx, email)
	created := false
	if errors.Is(err, pgx.ErrNoRows) {
		name := id.Name
		if name == "" {
			name = nameFrom(email)
		}
		c, err = q.CreateCustomer(ctx, sqlcgen.CreateCustomerParams{ID: uuid.Must(uuid.NewV7()), Email: email, Name: name})
		created = true
	}
	if err != nil {
		return Customer{}, false, err
	}
	err = q.CreateIdentity(ctx, sqlcgen.CreateIdentityParams{CustomerID: c.ID, Provider: "google", Subject: id.Subject, Email: &email})
	if code, constraint, ok := db.Violation(err); ok && code == "23505" && constraint == "customer_identities_customer_id_provider_key" {
		// The account already has a different Google login: one Google
		// account per customer (BR-127).
		return Customer{}, false, apperrors.Unauthenticated("This email's account signs in with another Google account.")
	}
	return c, created, err
}

// nameFrom is a new Google customer's name when Google gave none: the part
// of the email before the @.
func nameFrom(email string) string {
	for i, r := range email {
		if r == '@' {
			return email[:i]
		}
	}
	return email
}
