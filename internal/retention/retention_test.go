package retention

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

func TestNextRun(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, c := range []struct{ now, want string }{
		{"2026-10-12T01:59:59+07:00", "2026-10-12T02:00:00+07:00"},
		{"2026-10-12T02:00:00+07:00", "2026-10-13T02:00:00+07:00"},
		{"2026-10-12T23:30:00+07:00", "2026-10-13T02:00:00+07:00"},
		// 18:30 UTC is already 01:30 the next day in Jakarta.
		{"2026-10-12T18:30:00Z", "2026-10-13T02:00:00+07:00"},
		{"2026-12-31T03:00:00+07:00", "2027-01-01T02:00:00+07:00"},
	} {
		if got := nextRun(at(c.now)); !got.Equal(at(c.want)) || got.Location() != config.WIB {
			t.Errorf("nextRun(%s) = %s, want %s", c.now, got, c.want)
		}
	}
}

// seedShop is a shop with one cart and one session of each kind BR-096 names,
// each id recorded under its kind.
func seedShop(t *testing.T, store *db.Store) (uuid.UUID, map[string]uuid.UUID) {
	t.Helper()
	ctx := context.WithoutCancel(t.Context())
	id := uuid.Must(uuid.NewV7())
	ids := map[string]uuid.UUID{}
	for _, k := range []string{"customer", "product", "variant", "expired cart", "open cart", "checked-out cart",
		"expired session", "revoked session", "live session"} {
		ids[k] = uuid.New()
	}
	err := store.InTenantTx(tenant.NewContext(ctx, id), func(tx pgx.Tx) error {
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO tenants (id, name, slug, order_prefix) VALUES ($1, 'Retention', $2, 'RT')`, []any{id, "rt-" + id.String()}},
			{`INSERT INTO customers (id, tenant_id, email, name) VALUES ($1, $2, $3, 'Rina')`, []any{ids["customer"], id, id.String() + "@example.com"}},
			{`INSERT INTO products (id, tenant_id, title, slug, status) VALUES ($1, $2, 'Tee', 'tee', 'active')`, []any{ids["product"], id}},
			{`INSERT INTO variants (id, tenant_id, product_id, option_values, regular_price_amount) VALUES ($1, $2, $3, '{}', 100)`, []any{ids["variant"], id, ids["product"]}},
			{`INSERT INTO carts (id, tenant_id, status, expires_at) VALUES
			    ($1, $4, 'open', now() - interval '1 minute'),
			    ($2, $4, 'open', now() + interval '1 day'),
			    ($3, $4, 'checked_out', now() - interval '60 days')`,
				[]any{ids["expired cart"], ids["open cart"], ids["checked-out cart"], id}},
			{`INSERT INTO cart_items (tenant_id, cart_id, variant_id, qty) VALUES ($1, $2, $3, 1)`, []any{id, ids["expired cart"], ids["variant"]}},
			{`INSERT INTO customer_sessions (id, tenant_id, customer_id, refresh_token_hash, expires_at, revoked_at) VALUES
			    ($1, $4, $5, gen_random_uuid()::text, now() - interval '1 minute', NULL),
			    ($2, $4, $5, gen_random_uuid()::text, now() + interval '1 day', now()),
			    ($3, $4, $5, gen_random_uuid()::text, now() + interval '1 day', NULL)`,
				[]any{ids["expired session"], ids["revoked session"], ids["live session"], id, ids["customer"]}},
		} {
			if _, err := tx.Exec(ctx, q.sql, q.args...); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() {
		_ = store.InTenantTx(tenant.NewContext(ctx, id), func(tx pgx.Tx) error {
			for _, q := range []string{"DELETE FROM cart_items", "DELETE FROM carts", "DELETE FROM customer_sessions",
				"DELETE FROM customers", "DELETE FROM variants", "DELETE FROM products"} {
				if _, err := tx.Exec(ctx, q); err != nil {
					return err
				}
			}
			return nil
		})
	})
	return id, ids
}

// left is which of the seeded rows are still there, and the audit rows the
// sweep wrote.
func left(t *testing.T, store *db.Store, shop uuid.UUID, ids map[string]uuid.UUID) (kept []string, items int, audits []string) {
	t.Helper()
	ctx := t.Context()
	err := store.InTenantTx(tenant.NewContext(ctx, shop), func(tx pgx.Tx) error {
		for k, v := range ids {
			var n int
			if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM carts WHERE id = $1) + (SELECT count(*) FROM customer_sessions WHERE id = $1)`, v).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				kept = append(kept, k)
			}
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM cart_items`).Scan(&items); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT after::text FROM audit_log WHERE action = 'retention.sweep' AND actor_id IS NULL`)
		if err != nil {
			return err
		}
		audits, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(kept)
	return kept, items, audits
}

// TestSweep is P1-214's acceptance (BR-096): expired open carts and ended
// sessions go, checked-out carts stay, and each shop is swept inside its own
// tenant, leaving the next one alone until its turn.
func TestSweep(t *testing.T) {
	store, err := db.New(t.Context(), config.AppDatabaseURL())
	if err != nil {
		t.Fatalf("connect as app_user: %v", err)
	}
	t.Cleanup(store.Close)
	a, aIDs := seedShop(t, store)
	b, bIDs := seedShop(t, store)

	all, err := store.TenantIDs(t.Context())
	if err != nil || !slices.Contains(all, a) || !slices.Contains(all, b) {
		t.Fatalf("TenantIDs is missing a shop: %v", err)
	}

	if err := sweepShop(t.Context(), store, a); err != nil {
		t.Fatal(err)
	}
	want := []string{"checked-out cart", "live session", "open cart"}
	kept, items, audits := left(t, store, a, aIDs)
	if !slices.Equal(kept, want) || items != 0 {
		t.Errorf("shop A kept %v and %d items, want %v and none", kept, items, want)
	}
	if len(audits) != 1 || audits[0] != `{"carts": 1, "customer_sessions": 2}` {
		t.Errorf("shop A audit: %v", audits)
	}

	if kept, items, _ := left(t, store, b, bIDs); len(kept) != 6 || items != 1 {
		t.Errorf("sweeping shop A touched shop B: kept %v, %d items", kept, items)
	}

	// Again: nothing left to delete, and nothing more recorded.
	if err := sweepShop(t.Context(), store, a); err != nil {
		t.Fatal(err)
	}
	if _, _, audits := left(t, store, a, aIDs); len(audits) != 1 {
		t.Errorf("an empty sweep was audited: %v", audits)
	}
}
