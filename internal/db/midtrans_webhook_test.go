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

// TestResolveMidtransWebhook is the fourth pre-tenant read (BR-003): a
// notification URL's webhook id resolves to its shop with no tenant in
// context, only while the shop has a server key, and to three columns.
func TestResolveMidtransWebhook(t *testing.T) {
	ctx := t.Context()
	store, err := New(ctx, config.AppDatabaseURL())
	if err != nil {
		t.Fatalf("connect as app_user: %v", err)
	}
	t.Cleanup(store.Close)

	tenantID, hook := uuid.Must(uuid.NewV7()), uuid.New()
	tctx := tenant.NewContext(ctx, tenantID)
	exec := func(sql string, args ...any) {
		t.Helper()
		if err := store.InTenantTx(tctx, func(tx pgx.Tx) error { _, err := tx.Exec(ctx, sql, args...); return err }); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}
	exec(`INSERT INTO tenants (id, name, slug, order_prefix) VALUES ($1, 'Toko ABC', $2, 'TKA')`, tenantID, "toko-"+tenantID.String())
	exec(`INSERT INTO storefront_settings (tenant_id, midtrans_webhook_id, midtrans_environment) VALUES ($1, $2, 'sandbox')`, tenantID, hook)
	t.Cleanup(func() {
		bg := context.WithoutCancel(tctx)
		_ = store.InTenantTx(bg, func(tx pgx.Tx) error {
			_, _ = tx.Exec(bg, `DELETE FROM storefront_settings`)
			_, err := tx.Exec(bg, `DELETE FROM tenants WHERE id = $1`, tenantID)
			return err
		})
	})

	if _, err := store.ResolveMidtransWebhook(ctx, hook); !errors.Is(err, ErrNotFound) {
		t.Errorf("a shop with no server key resolves: %v", err)
	}
	exec(`UPDATE storefront_settings SET midtrans_server_key = '\x0102'`)
	w, err := store.ResolveMidtransWebhook(ctx, hook)
	if err != nil || w.TenantID != tenantID || w.Environment == nil || *w.Environment != "sandbox" || string(w.ServerKey) != "\x01\x02" {
		t.Fatalf("resolve: %+v %v", w, err)
	}
	if _, err := store.ResolveMidtransWebhook(ctx, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown webhook id: %v", err)
	}

	var result string
	var definer bool
	if err := store.pool.QueryRow(ctx, `SELECT pg_get_function_result(p.oid), p.prosecdef
		FROM pg_proc p WHERE p.proname = 'resolve_midtrans_webhook'`).Scan(&result, &definer); err != nil {
		t.Fatalf("read function: %v", err)
	}
	if want := "TABLE(tenant_id uuid, midtrans_environment text, midtrans_server_key bytea)"; result != want || !definer {
		t.Errorf("returns %s (definer %v)\nwant    %s, SECURITY DEFINER", result, definer, want)
	}
	var n int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM storefront_settings WHERE midtrans_webhook_id = $1`, hook).Scan(&n); err == nil && n != 0 {
		t.Errorf("app_user read %d settings row(s) with no tenant set", n)
	}
}
