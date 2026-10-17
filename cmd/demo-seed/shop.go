package main

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/team"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// teamCtx is the new shop and the staff who act in it.
type teamCtx struct {
	tenantID                  uuid.UUID
	ctx                       context.Context // the shop, no actor: a shopper or a webhook
	owner, admin, ops, former uuid.UUID
	logins                    []string
	oldKey                    uuid.UUID // the API key retire revokes
}

// as is the shop with a staff member acting, so the audit log names them.
func (t *teamCtx) as(user uuid.UUID) context.Context {
	return tenant.NewActorContext(t.ctx, tenant.Actor{UserID: user})
}

// shopAndTeam inserts the shop, its owner and its staff in one transaction, as
// create-tenant does, so a taken slug or email leaves nothing behind.
func (sd *seeder) shopAndTeam(ctx context.Context, s shop, password string, now time.Time) (*teamCtx, error) {
	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, err
	}
	t := &teamCtx{tenantID: uuid.Must(uuid.NewV7())}
	t.ctx = tenant.NewContext(ctx, t.tenantID)
	err = sd.store.InTenantTx(t.ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO tenants (id, name, slug, order_prefix, created_at) VALUES ($1, $2, $3, $4, $5)`,
			t.tenantID, s.Name, s.Slug, s.OrderPrefix, now.Add(-100*day)); err != nil {
			return fmt.Errorf("insert shop: %w", err)
		}
		insert := func(email, name, role, status string, hash *string, joined time.Time, seen *time.Time) (uuid.UUID, error) {
			id := uuid.Must(uuid.NewV7())
			_, err := tx.Exec(ctx, `INSERT INTO users (id, tenant_id, email, password_hash, name, role, status, created_at, last_login_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, id, t.tenantID, email, hash, name, role, status, joined, seen)
			if err != nil {
				return id, fmt.Errorf("insert %s: %w", email, err)
			}
			return id, nil
		}
		seen := now.Add(-3 * time.Hour)
		if t.owner, err = insert(s.OwnerEmail, "Demo Owner", "owner", "active", &hash, now.Add(-100*day), &seen); err != nil {
			return err
		}
		t.logins = append(t.logins, s.OwnerEmail+" (owner)")
		for i, m := range staff {
			email := fmt.Sprintf("%s@%s.example.com", m.local, s.Slug)
			joined, seen := now.Add(-time.Duration(95-i*10)*day), now.Add(-time.Duration(i+1)*5*time.Hour)
			h, last := &hash, &seen
			switch m.status {
			case "invited": // no password until the invitation is accepted
				h, last = nil, nil
			case "disabled":
				gone := now.Add(-20 * day)
				last = &gone
			}
			id, err := insert(email, m.name, m.role, m.status, h, joined, last)
			if err != nil {
				return err
			}
			switch {
			case m.status == "disabled":
				t.former = id
			case m.role == "admin":
				t.admin = id
			case m.role == "ops" && m.status == "active":
				t.ops = id
			}
			if m.status == "active" {
				t.logins = append(t.logins, fmt.Sprintf("%s (%s)", email, m.role))
			}
		}
		return nil
	})
	return t, err
}

// settings fills the shop's settings and storefront settings, and mints the
// storefront's API keys: the live one, and an old one retire revokes.
func (sd *seeder) settings(ctx context.Context, t *teamCtx) error {
	tz := "Asia/Jakarta"
	if _, err := sd.team.UpdateSettings(ctx, nil, &tz, nil); err != nil {
		return err
	}
	str := func(s string) team.Change[string] { return team.Change[string]{Set: true, Value: &s} }
	couriers, on := []string{"jne", "jnt", "sicepat", "anteraja"}, true
	if _, err := sd.team.UpdateStorefrontSettings(ctx, team.StorefrontPatch{
		ContactEmail:     str("halo@example.com"),
		OriginPostalCode: str("40115"),
		BankTransferInstructions: str("Transfer ke BCA 123 456 7890 a.n. Toko Demo Belli, lalu kirim bukti transfer " +
			"lewat WhatsApp ke 0812 0000 0000. Pesanan diproses setelah dana masuk."),
		ShippingCouriers:    &couriers,
		BankTransferEnabled: &on,
	}); err != nil {
		return err
	}
	if _, _, err := sd.team.CreateAPIKey(ctx, "Website toko", "https://toko.example.com"); err != nil {
		return err
	}
	old, _, err := sd.team.CreateAPIKey(ctx, "Website lama", "https://lama.example.com")
	t.oldKey = old.ID
	return err
}

// shiftBack moves everything the shop holds so far -- settings, keys, the
// catalog and their audit rows -- back by the given span, so it reads as set
// up before the first order rather than this morning. It runs before any
// customer or order exists, so no WHERE is needed: RLS narrows each statement
// to this shop. Image jobs stay put: the worker is still finishing them.
func (sd *seeder) shiftBack(ctx context.Context, by time.Duration) error {
	return sd.store.InTenantTx(ctx, func(tx pgx.Tx) error {
		for _, stmt := range []string{
			`UPDATE brands SET created_at = created_at - make_interval(secs => $1), updated_at = updated_at - make_interval(secs => $1)`,
			`UPDATE categories SET created_at = created_at - make_interval(secs => $1), updated_at = updated_at - make_interval(secs => $1)`,
			`UPDATE products SET created_at = created_at - make_interval(secs => $1), updated_at = updated_at - make_interval(secs => $1)`,
			`UPDATE variants SET created_at = created_at - make_interval(secs => $1), updated_at = updated_at - make_interval(secs => $1)`,
			`UPDATE product_media SET created_at = created_at - make_interval(secs => $1)`,
			`UPDATE api_keys SET created_at = created_at - make_interval(secs => $1)`,
			`UPDATE storefront_settings SET updated_at = updated_at - make_interval(secs => $1)`,
			`UPDATE audit_log SET created_at = created_at - make_interval(secs => $1)`,
		} {
			if _, err := tx.Exec(ctx, stmt, by.Seconds()); err != nil {
				return err
			}
		}
		return nil
	})
}
