package httpapi_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/secrets"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

func init() {
	isolationCases = append(isolationCases,
		isolationCase{route: route{"GET", "/v1/storefront-settings"}, seed: seedStorefrontSettings,
			request: func(t *testing.T, s seeded) *http.Request {
				return bearerRequest(t, http.MethodGet, "/v1/storefront-settings", s.accessToken)
			}},
		isolationCase{route: route{"PATCH", "/v1/storefront-settings"}, seed: seedStorefrontSettings,
			request: func(t *testing.T, s seeded) *http.Request {
				return bodyRequest(t, http.MethodPatch, "/v1/storefront-settings", s.accessToken, map[string]any{"origin_postal_code": "60231"})
			}},
	)
	auditCases = append(auditCases, auditCase{route: route{"PATCH", "/v1/storefront-settings"},
		request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			return bodyRequest(t, http.MethodPatch, "/v1/storefront-settings", s.accessToken,
				map[string]any{"bank_transfer_instructions": "Transfer to BCA " + uuid.NewString()}), "tenant", s.tenantID
		}})
}

// testBox seals with a fixed test key, as cmd/api does with ENCRYPTION_KEY.
func testBox(t *testing.T) *secrets.Box {
	t.Helper()
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

// seedStorefrontSettings is a signed-in owner whose shop's contact email
// names the tenant, so it is the marker.
func seedStorefrontSettings(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID) seeded {
	t.Helper()
	s := seedSignedOwner(ctx, t, store, tenantID)
	s.marker = "shop-" + tenantID.String() + "@example.com"
	execAs(t, store, tenantID, `INSERT INTO storefront_settings (tenant_id, midtrans_webhook_id, contact_email)
		VALUES (current_setting('app.tenant_id')::uuid, gen_random_uuid(), '`+s.marker+`')`)
	return s
}

// execAs runs sql in tenantID, and removes the shop's settings row when the
// test ends so the tenant itself can go.
func execAs(t *testing.T, store *db.Store, tenantID uuid.UUID, sql string) {
	t.Helper()
	ctx := t.Context()
	if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, sql)
		return err
	}); err != nil {
		t.Fatalf("exec: %v", err)
	}
	cleanSettings(t, store, tenantID)
}

// cleanSettings deletes a test shop's settings row before its tenant goes
// (cleanups run last-registered first).
func cleanSettings(t *testing.T, store *db.Store, tenantID uuid.UUID) {
	t.Cleanup(func() {
		c := context.WithoutCancel(t.Context())
		_ = store.InTenantTx(tenant.NewContext(c, tenantID), func(tx pgx.Tx) error {
			_, err := tx.Exec(c, `DELETE FROM storefront_settings`)
			return err
		})
	})
}

// TestStorefrontSettings is P1-216's acceptance (04-api-spec.md §4; BR-120,
// BR-122, BR-129).
func TestStorefrontSettings(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	tenantID := uuid.Must(uuid.NewV7())
	owner := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleOwner)
	cleanSettings(t, store, tenantID)
	do := apiClient(t, owner)

	code, st := do("GET", "/v1/storefront-settings", nil)
	if code != 200 || st["bank_transfer_enabled"] != true || st["midtrans_enabled"] != false || st["midtrans_server_key_set"] != false ||
		!strings.HasPrefix(st["midtrans_notification_url"].(string), "http://localhost:8080/v1/webhooks/midtrans/") {
		t.Fatalf("a new shop's row: %d %v", code, st)
	}
	hook := st["midtrans_notification_url"].(string)
	if id := uuid.MustParse(hook[strings.LastIndex(hook, "/")+1:]); id.Version() != 4 {
		t.Errorf("webhook id is v%d; it sits in a URL, so it is a random v4", id.Version())
	}

	code, st = do("PATCH", "/v1/storefront-settings", map[string]any{
		"contact_email": "halo@tokoabc.com", "origin_postal_code": "40115", "shipping_couriers": []string{"jne", "sicepat"},
		"bank_transfer_instructions": "Transfer to BCA 123", "midtrans_environment": "sandbox",
		"midtrans_client_key": "Mid-client-abc", "midtrans_server_key": "Mid-server-SECRET", "midtrans_enabled": true})
	if code != 200 || st["midtrans_enabled"] != true || st["midtrans_server_key_set"] != true || st["origin_postal_code"] != "40115" {
		t.Fatalf("patch: %d %v", code, st)
	}
	if _, has := st["midtrans_server_key"]; has {
		t.Error("the server key came back in a response (BR-129)")
	}
	if st["midtrans_notification_url"] != hook {
		t.Error("the notification URL changed on a PATCH")
	}

	// Stored sealed, for this shop only, and it opens to what was sent.
	var stored []byte
	_ = store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT midtrans_server_key FROM storefront_settings`).Scan(&stored)
	})
	if bytes.Contains(stored, []byte("Mid-server-SECRET")) {
		t.Fatal("the server key is stored in the clear")
	}
	if plain, err := testBox(t).Open(tenantID, stored); err != nil || string(plain) != "Mid-server-SECRET" {
		t.Errorf("sealed key opens to %q %v", plain, err)
	}

	for what, req := range map[string]struct {
		body  map[string]any
		field string
	}{
		"both methods off":            {map[string]any{"bank_transfer_enabled": false, "midtrans_enabled": false}, "bank_transfer_enabled"},
		"remove the key, Midtrans on": {map[string]any{"midtrans_server_key": nil}, "midtrans_enabled"},
		"a bad postal code":           {map[string]any{"origin_postal_code": "401"}, "origin_postal_code"},
		"an unknown courier":          {map[string]any{"shipping_couriers": []string{"jne", "dhl"}}, "shipping_couriers"},
		"a bad environment":           {map[string]any{"midtrans_environment": "staging"}, "midtrans_environment"},
		"a bad contact email":         {map[string]any{"contact_email": "nope"}, "contact_email"},
		"a blank server key":          {map[string]any{"midtrans_server_key": " "}, "midtrans_server_key"},
		"null couriers":               {map[string]any{"shipping_couriers": nil}, "shipping_couriers"},
		"a server-managed field":      {map[string]any{"midtrans_server_key_set": true}, "midtrans_server_key_set"},
	} {
		t.Run(what, func(t *testing.T) {
			code, p := do("PATCH", "/v1/storefront-settings", req.body)
			assertProblem(t, code, p, 422, "validation_failed", req.field)
		})
	}

	// Midtrans off, then the key can go.
	code, st = do("PATCH", "/v1/storefront-settings", map[string]any{"midtrans_enabled": false, "midtrans_server_key": nil})
	if code != 200 || st["midtrans_server_key_set"] != false {
		t.Errorf("remove the key: %d %v", code, st)
	}

	// settings:read is owner, admin and viewer; settings:write is the owner's.
	for role, wantGet := range map[string]int{auth.RoleAdmin: 200, auth.RoleViewer: 200, auth.RoleOps: 403} {
		u := signInAnotherUser(ctx, t, store, tenantID, role)
		if code, _ := apiClient(t, u)("GET", "/v1/storefront-settings", nil); code != wantGet {
			t.Errorf("%s GET: %d, want %d", role, code, wantGet)
		}
		code, p := apiClient(t, u)("PATCH", "/v1/storefront-settings", map[string]any{"contact_email": "x@example.com"})
		assertProblem(t, code, p, 403, "permission_denied", "")
	}
}

// TestServerKeyNeverLogged extends P1-227's log scan over the route that
// takes the key: a PATCH that succeeds and one that fails write no byte of it
// to the log, the audit row or the response (BR-013, BR-129).
func TestServerKeyNeverLogged(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	tenantID := uuid.Must(uuid.NewV7())
	owner := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleOwner)
	cleanSettings(t, store, tenantID)

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	const key = "Mid-server-NEVER-LOGGED-0123"
	do := apiClient(t, owner)
	_, ok := do("PATCH", "/v1/storefront-settings", map[string]any{"midtrans_server_key": key})
	_, bad := do("PATCH", "/v1/storefront-settings", map[string]any{"midtrans_server_key": key, "origin_postal_code": "x"})
	var audit string
	_ = store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT coalesce(string_agg(coalesce(before::text, '') || coalesce(after::text, ''), ''), '')
			FROM audit_log WHERE action = 'storefront_settings.update'`).Scan(&audit)
	})
	for where, text := range map[string]string{"the log": logs.String(), "the audit log": audit,
		"a response": fmt.Sprint(ok, bad)} {
		if strings.Contains(text, key) {
			t.Errorf("the server key is in %s", where)
		}
	}
	if !strings.Contains(audit, "midtrans_server_key_set") {
		t.Errorf("the audit row does not say the key was set: %s", audit)
	}
}
