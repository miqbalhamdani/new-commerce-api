package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// Category routes (P1-024) in the isolation and audit suites.
func init() {
	byID := func(method string, body any) func(t *testing.T, s seeded) *http.Request {
		return func(t *testing.T, s seeded) *http.Request {
			if body == nil {
				return bearerRequest(t, method, "/v1/categories/"+s.otherID, s.accessToken)
			}
			return bodyRequest(t, method, "/v1/categories/"+s.otherID, s.accessToken, body)
		}
	}
	isolationCases = append(isolationCases,
		isolationCase{route: route{"GET", "/v1/categories"}, seed: seedCategory,
			request: func(t *testing.T, s seeded) *http.Request {
				return bearerRequest(t, http.MethodGet, "/v1/categories", s.accessToken)
			}},
		isolationCase{route: route{"POST", "/v1/categories"}, seed: seedCategory,
			request: func(t *testing.T, s seeded) *http.Request {
				// Aiming at B's category as a parent must be refused, not adopted.
				return bodyRequest(t, http.MethodPost, "/v1/categories", s.accessToken,
					map[string]any{"name": "Child", "parent_id": s.otherID})
			}},
		isolationCase{route: route{"GET", "/v1/categories/{id}"}, seed: seedCategory, request: byID(http.MethodGet, nil)},
		isolationCase{route: route{"PATCH", "/v1/categories/{id}"}, seed: seedCategory, request: byID(http.MethodPatch, map[string]any{"name": "Renamed"})},
		isolationCase{route: route{"DELETE", "/v1/categories/{id}"}, seed: seedCategory, request: byID(http.MethodDelete, nil)},
	)

	auditCases = append(auditCases,
		auditCase{route: route{"POST", "/v1/categories"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			return bodyRequest(t, http.MethodPost, "/v1/categories", s.accessToken, map[string]any{"name": "Audited"}), "category", ""
		}},
		auditCase{route: route{"PATCH", "/v1/categories/{id}"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			id := apiCreate(t, s, "/v1/categories", map[string]any{"name": "Before"})
			return bodyRequest(t, http.MethodPatch, "/v1/categories/"+id, s.accessToken, map[string]any{"name": "After"}), "category", id
		}},
		auditCase{route: route{"DELETE", "/v1/categories/{id}"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			id := apiCreate(t, s, "/v1/categories", map[string]any{"name": "Doomed"})
			return bearerRequest(t, http.MethodDelete, "/v1/categories/"+id, s.accessToken), "category", id
		}},
	)
}

// seedCategory is a signed-in admin plus one root category whose name is the
// marker.
func seedCategory(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID) seeded {
	t.Helper()
	s := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	id := uuid.Must(uuid.NewV7())
	s.marker = "Cat" + id.String()
	s.id = id.String()
	if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO categories (id, tenant_id, name) VALUES ($1, $2, $3)`, id, tenantID, s.marker)
		return err
	}); err != nil {
		t.Fatalf("seed category: %v", err)
	}
	return s
}

// TestCategories is P1-024's acceptance (04-api-spec.md §6.2; BR-008, BR-032..036).
func TestCategories(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	tenantID := uuid.Must(uuid.NewV7())
	admin := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	do := apiClient(t, admin)

	create := func(body map[string]any) map[string]any {
		t.Helper()
		code, c := do("POST", "/v1/categories", body)
		if code != 201 {
			t.Fatalf("create %v: %d %v", body, code, c)
		}
		return c
	}
	apparel := create(map[string]any{"name": "Apparel"})
	outer := create(map[string]any{"name": "Outerwear", "parent_id": apparel["id"]})
	jackets := create(map[string]any{"name": "Jackets", "parent_id": outer["id"]})
	technical := create(map[string]any{"name": "Technical", "parent_id": apparel["id"]})
	series := create(map[string]any{"name": "SS26", "kind": "series"})
	if jackets["path"] != "apparel.outerwear.jackets" || series["kind"] != "series" {
		t.Fatalf("paths: %v %v", jackets["path"], series)
	}

	t.Run("path and kind are not writable", func(t *testing.T) {
		code, p := do("POST", "/v1/categories", map[string]any{"name": "X", "path": "x"})
		assertProblem(t, code, p, 422, "validation_failed", "path")
		code, p = do("PATCH", "/v1/categories/"+outer["id"].(string), map[string]any{"kind": "series"})
		assertProblem(t, code, p, 422, "unknown_field", "kind")
	})
	t.Run("a parent of another kind or tenant is 422 on parent_id", func(t *testing.T) {
		code, p := do("POST", "/v1/categories", map[string]any{"name": "Mixed", "parent_id": series["id"]})
		assertProblem(t, code, p, 422, "validation_failed", "parent_id")
		code, p = do("POST", "/v1/categories", map[string]any{"name": "Lost", "parent_id": uuid.NewString()})
		assertProblem(t, code, p, 422, "validation_failed", "parent_id")
	})
	t.Run("move rewrites descendants and states counts", func(t *testing.T) {
		code, moved := do("PATCH", "/v1/categories/"+outer["id"].(string), map[string]any{"parent_id": technical["id"]})
		if code != 200 || moved["path"] != "apparel.technical.outerwear" {
			t.Fatalf("move: %d %v", code, moved)
		}
		code, j := do("GET", "/v1/categories/"+jackets["id"].(string), nil)
		if code != 200 || j["path"] != "apparel.technical.outerwear.jackets" {
			t.Errorf("descendant after move: %d %v", code, j)
		}
		code, d := do("GET", "/v1/categories/"+apparel["id"].(string), nil)
		if code != 200 || d["descendant_count"] != float64(3) || d["product_count"] != float64(0) {
			t.Errorf("counts: %d %v", code, d)
		}
	})
	t.Run("moving beneath a descendant is 422 on parent_id", func(t *testing.T) {
		code, p := do("PATCH", "/v1/categories/"+apparel["id"].(string), map[string]any{"parent_id": jackets["id"]})
		assertProblem(t, code, p, 422, "validation_failed", "parent_id")
	})
	t.Run("parent_id null makes a root", func(t *testing.T) {
		code, c := do("PATCH", "/v1/categories/"+technical["id"].(string), map[string]any{"parent_id": nil})
		if code != 200 || c["path"] != "technical" || c["parent_id"] != nil {
			t.Fatalf("to root: %d %v", code, c)
		}
		if code, j := do("GET", "/v1/categories/"+jackets["id"].(string), nil); code != 200 || j["path"] != "technical.outerwear.jackets" {
			t.Errorf("descendant after re-root: %v", j["path"])
		}
	})
	t.Run("list filters by kind, parent and depth", func(t *testing.T) {
		count := func(path string) int {
			t.Helper()
			code, p := do("GET", path, nil)
			if code != 200 {
				t.Fatalf("%s: %d %v", path, code, p)
			}
			return len(p["data"].([]any))
		}
		if n := count("/v1/categories?kind=series"); n != 1 {
			t.Errorf("series: %d", n)
		}
		if n := count("/v1/categories?parent_id=" + technical["id"].(string)); n != 2 {
			t.Errorf("below technical: %d", n)
		}
		if n := count("/v1/categories?parent_id=" + technical["id"].(string) + "&depth=1"); n != 1 {
			t.Errorf("one level below technical: %d", n)
		}
		if n := count("/v1/categories?kind=category&depth=1"); n != 2 {
			t.Errorf("category roots: %d", n)
		}
		code, p := do("GET", "/v1/categories?kind=bogus", nil)
		assertProblem(t, code, p, 422, "validation_failed", "kind")
	})
	t.Run("a category in use is 409 with both counts", func(t *testing.T) {
		code, p := do("DELETE", "/v1/categories/"+technical["id"].(string), nil)
		assertProblem(t, code, p, 409, "category_in_use", "children")

		// A product linked to the leaf keeps it in use too.
		product := uuid.Must(uuid.NewV7())
		if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `INSERT INTO products (id, tenant_id, title, slug) VALUES ($1, $2, 'Tee', $3)`,
				product, tenantID, "tee-"+product.String()); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO product_categories (tenant_id, product_id, category_id) VALUES ($1, $2, $3)`,
				tenantID, product, jackets["id"])
			return err
		}); err != nil {
			t.Fatalf("link product: %v", err)
		}
		code, p = do("DELETE", "/v1/categories/"+jackets["id"].(string), nil)
		assertProblem(t, code, p, 409, "category_in_use", "products")

		code, _ = do("DELETE", "/v1/categories/"+series["id"].(string), nil)
		if code != 204 {
			t.Errorf("an unused category: %d", code)
		}
		if code, p := do("GET", "/v1/categories?kind=series", nil); code != 200 || len(p["data"].([]any)) != 0 {
			t.Errorf("archived category still listed: %v", p)
		}
	})
}
