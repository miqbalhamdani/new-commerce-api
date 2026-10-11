package storefront

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// TestKeyMiddleware is P1-202's acceptance (04-api-spec.md §11.2; BR-003,
// BR-083, BR-085), on a stub handler that echoes the tenant it was given.
func TestKeyMiddleware(t *testing.T) {
	ctx := t.Context()
	store := openStore(t)
	tenantID, key := seedKey(t, store, "https://tokoabc.com")

	keys := NewKeys(store)
	clock := time.Now()
	keys.now = func() time.Time { return clock }
	h := keys.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := tenant.FromContext(r.Context())
		_, _ = w.Write([]byte(id.String()))
	}))
	send := func(method, apiKey, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/v1/storefront/products", nil)
		if apiKey != "" {
			r.Header.Set("X-Api-Key", apiKey)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	for name, apiKey := range map[string]string{"missing": "", "unknown": "sf_live_nope"} {
		rec := send("GET", apiKey, "")
		assertCode(t, name+" key", rec, 401, "invalid_api_key")
	}
	if len(keys.cache) != 0 {
		t.Errorf("an unknown key was cached: %d entries", len(keys.cache))
	}

	// The website's own server: no Origin, accepted, tenant from the key.
	rec := send("GET", key, "")
	if rec.Code != 200 || rec.Body.String() != tenantID.String() {
		t.Fatalf("server call: %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" || rec.Header().Get("Vary") != "Origin" {
		t.Errorf("server call headers: %v", rec.Header())
	}
	var used *time.Time
	_ = store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT last_used_at FROM api_keys WHERE tenant_id = $1`, tenantID).Scan(&used)
	})
	if used == nil {
		t.Error("last_used_at not recorded")
	}

	// The key's website: echoed, never '*'.
	rec = send("GET", key, "https://tokoabc.com")
	if rec.Code != 200 || rec.Header().Get("Access-Control-Allow-Origin") != "https://tokoabc.com" {
		t.Errorf("own origin: %d %v", rec.Code, rec.Header())
	}

	// Anyone else's: refused, with no ACAO, so the page cannot read it.
	for _, origin := range []string{"https://evil.test", "http://tokoabc.com", "https://tokoabc.com:8443"} {
		rec = send("GET", key, origin)
		assertCode(t, origin, rec, 403, "origin_not_allowed")
		if acao := rec.Header().Get("Access-Control-Allow-Origin"); acao != "" {
			t.Errorf("%s: refused with ACAO %q", origin, acao)
		}
	}

	// Preflight: no key, any origin.
	rec = send("OPTIONS", "", "https://anyone.test")
	if rec.Code != 204 || rec.Header().Get("Access-Control-Allow-Origin") != "https://anyone.test" ||
		!strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "X-Api-Key") {
		t.Errorf("preflight: %d %v", rec.Code, rec.Header())
	}

	// Revoked: the cache keeps it alive for at most the TTL (BR-085).
	_ = store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE api_keys SET revoked_at = now() WHERE tenant_id = $1`, tenantID)
		return err
	})
	clock = clock.Add(KeyCacheTTL - time.Second)
	if rec = send("GET", key, ""); rec.Code != 200 {
		t.Errorf("inside the cache window: %d", rec.Code)
	}
	clock = clock.Add(2 * time.Second)
	assertCode(t, "revoked", send("GET", key, ""), 401, "invalid_api_key")
}

// TestRouterAnswersProblems: past the key, a route storefront.yaml does not
// define is a problem+json 404, not chi's plain text; before it, the key
// still comes first.
func TestRouterAnswersProblems(t *testing.T) {
	store := openStore(t)
	_, key := seedKey(t, store, "https://tokoabc.com")
	h := NewRouter(NewServer(), NewKeys(store))

	r := httptest.NewRequest("GET", "/v1/storefront/nothing-here", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	assertCode(t, "no key", rec, 401, "invalid_api_key")

	r.Header.Set("X-Api-Key", key)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	assertCode(t, "unknown route", rec, 404, "not_found")
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("content type %q", ct)
	}
}

// TestAdminAndStorefrontDoNotImportEachOther is P1-202's package boundary:
// a handler shared between the two audiences is how a staff capability
// leaks to the public (CLAUDE.md import rules).
func TestAdminAndStorefrontDoNotImportEachOther(t *testing.T) {
	const mod = "github.com/miqbalhamdani/new-commerce-api/internal/"
	for pkg, forbidden := range map[string]string{"storefront": mod + "http", "http": mod + "storefront"} {
		out, err := exec.Command("go", "list", "-deps", "./../"+pkg).Output()
		if err != nil {
			t.Fatalf("go list %s: %v", pkg, err)
		}
		for _, dep := range strings.Fields(string(out)) {
			if dep == forbidden {
				t.Errorf("internal/%s imports %s", pkg, forbidden)
			}
		}
	}
}

func openStore(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.New(t.Context(), config.AppDatabaseURL())
	if err != nil {
		t.Fatalf("connect as app_user: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

// seedKey makes a shop with one live key for origin, and returns the key's
// plaintext.
func seedKey(t *testing.T, store *db.Store, origin string) (uuid.UUID, string) {
	t.Helper()
	ctx := t.Context()
	tenantID := uuid.Must(uuid.NewV7())
	key := "sf_live_test-" + uuid.NewString()
	err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO tenants (id, name, slug, order_prefix) VALUES ($1, 'Storefront', $2, 'SF')`,
			tenantID, "sf-"+tenantID.String()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO api_keys (id, tenant_id, name, key_hash, allowed_origin) VALUES ($1, $2, 'Test', $3, $4)`,
			uuid.Must(uuid.NewV7()), tenantID, auth.HashRefreshToken(key), origin)
		return err
	})
	if err != nil {
		t.Fatalf("seed key: %v", err)
	}
	t.Cleanup(func() {
		c := context.WithoutCancel(ctx)
		_ = store.InTenantTx(tenant.NewContext(c, tenantID), func(tx pgx.Tx) error {
			_, _ = tx.Exec(c, `DELETE FROM api_keys WHERE tenant_id = $1`, tenantID)
			_, err := tx.Exec(c, `DELETE FROM tenants WHERE id = $1`, tenantID)
			return err
		})
	})
	return tenantID, key
}

func assertCode(t *testing.T, what string, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var p struct{ Type string }
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if rec.Code != status || !strings.HasSuffix(p.Type, "/"+code) {
		t.Errorf("%s: %d %s, want %d %s", what, rec.Code, rec.Body, status, code)
	}
}
