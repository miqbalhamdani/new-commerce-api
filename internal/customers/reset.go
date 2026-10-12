package customers

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/email"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// Password reset (P1-207, 04-api-spec.md §11.8, BR-094).

// RequestReset emails a reset link to the account with this email, if there
// is one. The caller answers 202 either way (BR-021). resetURL is the shop's
// own reset page: its origin must be the API key's, so a link in the shop's
// email can only ever lead to the shop's website.
func (s *Service) RequestReset(ctx context.Context, address, resetURL, allowedOrigin string) error {
	u, err := url.Parse(resetURL)
	if err != nil || u.User != nil || u.Scheme+"://"+u.Host != allowedOrigin {
		return fieldError("reset_url", "reset_url is a page on "+allowedOrigin+", the website this API key is for")
	}
	var c Customer
	err = s.tx(ctx, func(q *sqlcgen.Queries) (err error) {
		c, err = q.CustomerByEmail(ctx, normalEmail(address))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	shop, _ := tenant.FromContext(ctx)
	return s.mail(ctx, email.Envelope{Kind: "password_reset", TenantID: shop, CustomerID: c.ID, URL: resetURL})
}

// ConfirmReset sets a new password with a reset token and signs the customer
// out everywhere (BR-094). Every way a token can fail is one 422 on token.
func (s *Service) ConfirmReset(ctx context.Context, token, password string) error {
	if len(password) < 8 {
		return fieldError("password", "password is at least 8 characters")
	}
	r, err := s.resets.Parse(token, time.Now())
	if shop, _ := tenant.FromContext(ctx); err != nil || r.TenantID != shop {
		return invalidReset()
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	return s.tx(ctx, func(q *sqlcgen.Queries) error {
		c, err := q.LockCustomer(ctx, r.CustomerID)
		if errors.Is(err, pgx.ErrNoRows) {
			return invalidReset()
		}
		if err != nil {
			return err
		}
		if !s.resets.Verify(token, c.PasswordHash) {
			return invalidReset()
		}
		// Guarded on the old hash as well as locked: a link verifies against
		// exactly one hash, so it sets a password exactly once.
		n, err := q.SetCustomerPassword(ctx, sqlcgen.SetCustomerPasswordParams{ID: c.ID, NewHash: &hash, OldHash: c.PasswordHash})
		if err != nil {
			return err
		}
		if n == 0 {
			return invalidReset()
		}
		return q.RevokeCustomerSessions(ctx, c.ID)
	})
}

func invalidReset() error {
	return fieldError("token", "This reset link has expired or was already used. Ask for a new one.")
}

// ResetMail renders a password_reset envelope in the worker (BR-094,
// BR-128). The token is minted here, against the customer's hash as it is
// now, so it never travels through Redis. An account gone since the request
// gets nothing.
func ResetMail(store *db.Store, resets *auth.ResetTokens) email.Render {
	return func(ctx context.Context, e email.Envelope) (*email.Message, error) {
		tctx := tenant.NewContext(ctx, e.TenantID)
		var (
			c       Customer
			shop    sqlcgen.Tenant
			replyTo *string
		)
		err := store.InTenantTx(tctx, func(tx pgx.Tx) (err error) {
			q := sqlcgen.New(tx)
			if c, err = q.CustomerByID(tctx, e.CustomerID); err != nil {
				return err
			}
			if st, err := q.GetStorefrontSettings(tctx); err == nil {
				replyTo = st.ContactEmail
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			shop, err = q.GetTenant(tctx, e.TenantID)
			return err
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		link, err := url.Parse(e.URL)
		if err != nil {
			return nil, nil
		}
		query := link.Query()
		query.Set("token", resets.Mint(c.ID, e.TenantID, c.PasswordHash, time.Now()))
		link.RawQuery = query.Encode()
		subject, text, html := email.PasswordReset(shop.Name, c.Name, link.String())
		msg := &email.Message{FromName: shop.Name, To: c.Email, Subject: subject, Text: text, HTML: html}
		if replyTo != nil {
			msg.ReplyTo = *replyTo // the shop's contact email (BR-128)
		}
		return msg, nil
	}
}
