package httpapi_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
)

func init() {
	isolationCases = append(isolationCases,
		isolationCase{route: route{"GET", "/v1/settings"}, seed: seedSignedOwner,
			request: func(t *testing.T, s seeded) *http.Request {
				return bearerRequest(t, http.MethodGet, "/v1/settings", s.accessToken)
			}},
		isolationCase{route: route{"PATCH", "/v1/settings"}, seed: seedSignedOwner,
			request: func(t *testing.T, s seeded) *http.Request {
				return bodyRequest(t, http.MethodPatch, "/v1/settings", s.accessToken, map[string]any{"name": "Renamed"})
			}},
	)
	auditCases = append(auditCases, auditCase{route: route{"PATCH", "/v1/settings"},
		request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			return bodyRequest(t, http.MethodPatch, "/v1/settings", s.accessToken, map[string]any{"timezone": "Asia/Makassar"}),
				"tenant", s.tenantID
		}})
}

// TestSettings is P1-071's acceptance: only the owner can PATCH (BR-023,
// BR-029). And P1-702's: the slug is the owner's to pick (BR-008, BR-140).
func TestSettings(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	tenantID := uuid.Must(uuid.NewV7())
	owner := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleOwner)
	do := apiClient(t, owner)

	code, st := do("GET", "/v1/settings", nil)
	if code != 200 || st["id"] != tenantID.String() || st["timezone"] != "Asia/Jakarta" || st["order_prefix"] != "TST" {
		t.Fatalf("get: %d %v", code, st)
	}
	code, st = do("PATCH", "/v1/settings", map[string]any{"name": "Erigo Apparel", "timezone": "Asia/Makassar", "order_prefix": "ERG"})
	if code != 200 || st["name"] != "Erigo Apparel" || st["timezone"] != "Asia/Makassar" || st["order_prefix"] != "ERG" {
		t.Fatalf("patch: %d %v", code, st)
	}
	for field, value := range map[string]any{"timezone": "Mars/Base", "order_prefix": "erg", "name": ""} {
		code, p := do("PATCH", "/v1/settings", map[string]any{field: value})
		assertProblem(t, code, p, 422, "validation_failed", field)
	}
	for _, field := range []string{"status", "id"} {
		code, p := do("PATCH", "/v1/settings", map[string]any{field: "x"})
		assertProblem(t, code, p, 422, "validation_failed", field)
	}

	slug := "toko-" + tenantID.String()[:8]
	code, st = do("PATCH", "/v1/settings", map[string]any{"slug": slug})
	if code != 200 || st["slug"] != slug {
		t.Fatalf("slug: %d %v", code, st)
	}
	other := uuid.Must(uuid.NewV7())
	seedSignedInUserWithRole(ctx, t, store, other, auth.RoleOwner)
	taken := "iso-" + other.String() // seedAuthUser's slug
	for _, bad := range []any{"ab", "-toko", "toko-", "Toko", "toko_abc", "toko.abc", "admin", "www", taken, nil} {
		code, p := do("PATCH", "/v1/settings", map[string]any{"slug": bad})
		assertProblem(t, code, p, 422, "validation_failed", "slug")
	}
	code, p := do("PATCH", "/v1/settings", map[string]any{"currency": "USD"})
	assertProblem(t, code, p, 422, "unknown_field", "currency")

	for role, wantGet := range map[string]int{auth.RoleAdmin: 200, auth.RoleViewer: 200, auth.RoleOps: 403} {
		u := signInAnotherUser(ctx, t, store, tenantID, role)
		if code, _ := apiClient(t, u)("GET", "/v1/settings", nil); code != wantGet {
			t.Errorf("%s GET: %d, want %d", role, code, wantGet)
		}
		code, p := apiClient(t, u)("PATCH", "/v1/settings", map[string]any{"name": "Nope"})
		assertProblem(t, code, p, 403, "permission_denied", "")
	}
}
