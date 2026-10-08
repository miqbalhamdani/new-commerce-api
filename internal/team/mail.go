// Package team is the people side of a shop: users, invitations, roles and
// the tenant's own settings (P1-064, P1-071), and the email they send.
package team

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/email"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// InvitationMail renders an invitation envelope (BR-026, BR-128). The worker
// mints the token here, so it never travels through Redis. A user who is no
// longer invited gets nothing: the link would not work anyway.
func InvitationMail(store *db.Store, invites *auth.InviteSigner, adminURL string) email.Render {
	return func(ctx context.Context, e email.Envelope) (*email.Message, error) {
		tctx := tenant.NewContext(ctx, e.TenantID)
		var user, inviter sqlcgen.User
		var shop sqlcgen.Tenant
		err := store.InTenantTx(tctx, func(tx pgx.Tx) (err error) {
			q := sqlcgen.New(tx)
			if user, err = q.GetUser(tctx, e.UserID); err != nil {
				return err
			}
			if inviter, err = q.GetUser(tctx, e.ActorID); err != nil {
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
		if user.Status != "invited" {
			return nil, nil
		}
		link := adminURL + "/accept-invite#token=" + invites.Mint(user.ID, user.TenantID, time.Now())
		subject, text, html := email.Invitation(shop.Name, inviter.Name, user.Role, link)
		// ponytail: Reply-To is the inviter until storefront_settings has the
		// shop's contact email (P1-216).
		return &email.Message{FromName: shop.Name, To: string(user.Email), ReplyTo: string(inviter.Email),
			Subject: subject, Text: text, HTML: html}, nil
	}
}
