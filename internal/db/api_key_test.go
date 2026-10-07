package db

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// TestResolveAPIKey is P1-019's acceptance: resolve_api_key crosses tenants
// with no tenant context, through SECURITY DEFINER, and returns only its four
// columns (BR-003, BR-028).
func TestResolveAPIKey(t *testing.T) {
	ctx := t.Context()
	store, err := New(ctx, config.AppDatabaseURL())
	if err != nil {
		t.Fatalf("connect as app_user: %v", err)
	}
	t.Cleanup(store.Close)

	tenantID, keyID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	hash := "test-hash-" + keyID.String()
	tctx := tenant.NewContext(ctx, tenantID)
	if err := store.InTenantTx(tctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO tenants (id, name, slug, order_prefix) VALUES ($1, 'Toko ABC', $2, 'TKA')`,
			tenantID, "toko-"+tenantID.String()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO api_keys (id, tenant_id, name, key_hash, allowed_origin)
			VALUES ($1, $2, 'Main website', $3, 'https://tokoabc.com')`, keyID, tenantID, hash)
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() {
		bg := context.WithoutCancel(tctx)
		_ = store.InTenantTx(bg, func(tx pgx.Tx) error {
			_, _ = tx.Exec(bg, `DELETE FROM api_keys WHERE id = $1`, keyID)
			_, err := tx.Exec(bg, `DELETE FROM tenants WHERE id = $1`, tenantID)
			return err
		})
	})

	t.Run("resolves with no tenant in context", func(t *testing.T) {
		k, err := store.ResolveAPIKey(ctx, hash)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if k.ID != keyID || k.TenantID != tenantID || k.AllowedOrigin != "https://tokoabc.com" || k.RevokedAt != nil {
			t.Errorf("got %+v", k)
		}
	})

	t.Run("an unknown hash is ErrNotFound", func(t *testing.T) {
		if _, err := store.ResolveAPIKey(ctx, "no-such-hash"); !errors.Is(err, ErrNotFound) {
			t.Errorf("got %v, want ErrNotFound", err)
		}
	})

	t.Run("returns exactly its four columns", func(t *testing.T) {
		var result string
		var definer bool
		if err := store.pool.QueryRow(ctx, `SELECT pg_get_function_result(p.oid), p.prosecdef
			FROM pg_proc p WHERE p.proname = 'resolve_api_key'`).Scan(&result, &definer); err != nil {
			t.Fatalf("read function: %v", err)
		}
		const want = "TABLE(id uuid, tenant_id uuid, allowed_origin text, revoked_at timestamp with time zone)"
		if result != want {
			t.Errorf("returns %s\nwant    %s", result, want)
		}
		if !definer {
			t.Error("resolve_api_key is not SECURITY DEFINER")
		}
	})

	// Fails closed either way: zero rows on a fresh connection, or an error on
	// a pooled one whose transaction-local setting has reset to ''.
	t.Run("a plain read without the function sees nothing", func(t *testing.T) {
		var n int
		err := store.pool.QueryRow(ctx, `SELECT count(*) FROM api_keys WHERE key_hash = $1`, hash).Scan(&n)
		if err == nil && n != 0 {
			t.Errorf("app_user read %d api_keys row(s) with no tenant set", n)
		}
	})

	t.Run("allowed_origin must be exactly scheme://host[:port]", func(t *testing.T) {
		for _, bad := range []string{"https://tokoabc.com/", "https://tokoabc.com/shop", "tokoabc.com", "https://*.tokoabc.com/x"} {
			err := store.InTenantTx(tctx, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `INSERT INTO api_keys (id, tenant_id, name, key_hash, allowed_origin)
					VALUES ($1, $2, 'bad', $3, $4)`, uuid.Must(uuid.NewV7()), tenantID, uuid.NewString(), bad)
				return err
			})
			if err == nil {
				t.Errorf("%q was accepted", bad)
			}
		}
	})
}
