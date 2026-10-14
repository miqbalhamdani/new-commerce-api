package httpapi_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

func init() {
	isolationCases = append(isolationCases,
		isolationCase{route: route{"GET", "/v1/api-keys"}, seed: seedAPIKey,
			request: func(t *testing.T, s seeded) *http.Request {
				return bearerRequest(t, http.MethodGet, "/v1/api-keys", s.accessToken)
			}},
		isolationCase{route: route{"POST", "/v1/api-keys"}, seed: seedAPIKey,
			request: func(t *testing.T, s seeded) *http.Request {
				return bodyRequest(t, http.MethodPost, "/v1/api-keys", s.accessToken,
					map[string]any{"name": "Iso", "allowed_origin": "https://iso.example"})
			}},
		// A and B aim at each other's key: RLS hides it, so it is 404.
		isolationCase{route: route{"PATCH", "/v1/api-keys/{id}"}, seed: seedAPIKey,
			request: func(t *testing.T, s seeded) *http.Request {
				return bodyRequest(t, http.MethodPatch, "/v1/api-keys/"+s.otherID, s.accessToken, map[string]any{"name": "Mine"})
			}},
		isolationCase{route: route{"DELETE", "/v1/api-keys/{id}"}, seed: seedAPIKey,
			request: func(t *testing.T, s seeded) *http.Request {
				return bearerRequest(t, http.MethodDelete, "/v1/api-keys/"+s.otherID, s.accessToken)
			}},
	)
	auditCases = append(auditCases,
		auditCase{route: route{"POST", "/v1/api-keys"},
			request: func(t *testing.T, s seeded) (*http.Request, string, string) {
				return bodyRequest(t, http.MethodPost, "/v1/api-keys", s.accessToken,
					map[string]any{"name": "Audit", "allowed_origin": "https://audit.example"}), "api_key", ""
			}},
		auditCase{route: route{"PATCH", "/v1/api-keys/{id}"},
			request: func(t *testing.T, s seeded) (*http.Request, string, string) {
				id := insertAPIKey(t.Context(), t, openAppStore(t.Context(), t), uuid.MustParse(s.tenantID), "Audit")
				return bodyRequest(t, http.MethodPatch, "/v1/api-keys/"+id, s.accessToken,
					map[string]any{"allowed_origin": "https://moved.example"}), "api_key", id
			}},
		auditCase{route: route{"DELETE", "/v1/api-keys/{id}"},
			request: func(t *testing.T, s seeded) (*http.Request, string, string) {
				id := insertAPIKey(t.Context(), t, openAppStore(t.Context(), t), uuid.MustParse(s.tenantID), "Audit")
				return bearerRequest(t, http.MethodDelete, "/v1/api-keys/"+id, s.accessToken), "api_key", id
			}},
	)
}

// seedAPIKey is a signed-in owner whose shop has one key named after the
// tenant, so the name is the marker.
func seedAPIKey(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID) seeded {
	t.Helper()
	s := seedSignedOwner(ctx, t, store, tenantID)
	s.marker = "key-" + tenantID.String()
	s.id = insertAPIKey(ctx, t, store, tenantID, s.marker)
	return s
}

func insertAPIKey(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID, name string) string {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO api_keys (id, tenant_id, name, key_hash, allowed_origin)
			VALUES ($1, $2, $3, $4, 'https://shop.example')`, id, tenantID, name, "hash-"+id.String())
		return err
	})
	if err != nil {
		t.Fatalf("insert api key: %v", err)
	}
	return id.String()
}

// TestAPIKeys is P1-200's acceptance: the plaintext comes back once, only its
// SHA-256 and last four are stored and resolves to the shop, a bad origin is 422 (BR-028,
// BR-083), and a revoked key is gone from the list and from editing.
func TestAPIKeys(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	tenantID := uuid.Must(uuid.NewV7())
	owner := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleOwner)
	do := apiClient(t, owner)

	code, k := do("POST", "/v1/api-keys", map[string]any{"name": " Main website ", "allowed_origin": "https://tokoabc.com"})
	key, _ := k["key"].(string)
	if code != 201 || !strings.HasPrefix(key, "sf_live_") || len(key) < 40 || k["name"] != "Main website" ||
		k["allowed_origin"] != "https://tokoabc.com" || k["revoked_at"] != nil || k["last_used_at"] != nil ||
		k["key_last4"] != key[len(key)-4:] {
		t.Fatalf("create: %d %v", code, k)
	}
	if by, _ := k["created_by"].(map[string]any); by == nil || by["name"] == "" {
		t.Errorf("created_by %v, want the owner as {id, name}", k["created_by"])
	}
	id := k["id"].(string)

	// Only the hash is stored, and it is what the storefront resolves.
	resolved, err := store.ResolveAPIKey(ctx, auth.HashRefreshToken(key))
	if err != nil || resolved.TenantID != tenantID || resolved.AllowedOrigin != "https://tokoabc.com" {
		t.Fatalf("resolve: %+v %v", resolved, err)
	}

	code, list := do("GET", "/v1/api-keys", nil)
	data, _ := list["data"].([]any)
	if code != 200 || len(data) != 1 {
		t.Fatalf("list: %d %v", code, list)
	}
	if _, has := data[0].(map[string]any)["key"]; has {
		t.Error("the list returns the key; it is shown once, at creation")
	}
	// The last four stay, so the list can show sf_live_••••42ab (BR-028).
	if got := data[0].(map[string]any)["key_last4"]; got != key[len(key)-4:] {
		t.Errorf("list key_last4 %v, want %q", got, key[len(key)-4:])
	}

	code, k = do("PATCH", "/v1/api-keys/"+id, map[string]any{"name": "Shop", "allowed_origin": "http://localhost:3001"})
	if code != 200 || k["name"] != "Shop" || k["allowed_origin"] != "http://localhost:3001" {
		t.Fatalf("patch: %d %v", code, k)
	}
	if _, has := k["key"]; has {
		t.Error("PATCH returns the key")
	}

	for _, origin := range []string{"", "tokoabc.com", "https://tokoabc.com/", "https://tokoabc.com/shop",
		"https://*.tokoabc.com", "https://TokoABC.com", "HTTPS://tokoabc.com", "https://tokoabc.com:443",
		"http://tokoabc.com:80", "https://user@tokoabc.com", "ftp://tokoabc.com", "https://tokoabc.com?x=1"} {
		code, p := do("POST", "/v1/api-keys", map[string]any{"name": "Bad", "allowed_origin": origin})
		assertProblem(t, code, p, 422, "validation_failed", "allowed_origin")
	}
	code, p := do("POST", "/v1/api-keys", map[string]any{"name": "", "allowed_origin": "https://a.example"})
	assertProblem(t, code, p, 422, "validation_failed", "name")
	code, p = do("PATCH", "/v1/api-keys/"+id, map[string]any{"allowed_origin": nil})
	assertProblem(t, code, p, 422, "validation_failed", "allowed_origin")
	code, p = do("POST", "/v1/api-keys", map[string]any{"name": "x", "allowed_origin": "https://a.example", "key": "sf_live_mine"})
	assertProblem(t, code, p, 422, "validation_failed", "key")
	code, p = do("POST", "/v1/api-keys", map[string]any{"name": "x", "allowed_origin": "https://a.example", "scopes": []string{"all"}})
	assertProblem(t, code, p, 422, "unknown_field", "scopes")

	if code, _ := do("DELETE", "/v1/api-keys/"+id, nil); code != 204 {
		t.Fatalf("revoke: %d", code)
	}
	if resolved, err := store.ResolveAPIKey(ctx, auth.HashRefreshToken(key)); err != nil || resolved.RevokedAt == nil {
		t.Errorf("revoked key resolves as %+v %v, want revoked_at set", resolved, err)
	}
	if _, list := do("GET", "/v1/api-keys", nil); len(list["data"].([]any)) != 0 {
		t.Errorf("revoked key still listed: %v", list)
	}
	code, p = do("DELETE", "/v1/api-keys/"+id, nil)
	assertProblem(t, code, p, 404, "not_found", "")
	code, p = do("PATCH", "/v1/api-keys/"+id, map[string]any{"name": "Back"})
	assertProblem(t, code, p, 404, "not_found", "")

	// api_keys:* is owner and admin only (04-api-spec.md §3).
	for role, want := range map[string]int{auth.RoleAdmin: 200, auth.RoleOps: 403, auth.RoleViewer: 403} {
		u := signInAnotherUser(ctx, t, store, tenantID, role)
		if code, _ := apiClient(t, u)("GET", "/v1/api-keys", nil); code != want {
			t.Errorf("%s GET: %d, want %d", role, code, want)
		}
	}
}
