package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// Product routes (P1-028) in the isolation and audit suites.
func init() {
	isolationCases = append(isolationCases,
		isolationCase{route: route{"POST", "/v1/products"}, seed: seedProduct,
			request: func(t *testing.T, s seeded) *http.Request {
				return bodyRequest(t, http.MethodPost, "/v1/products", s.accessToken, map[string]any{"title": "New"})
			}},
		isolationCase{route: route{"GET", "/v1/products/{id}"}, seed: seedProduct,
			request: func(t *testing.T, s seeded) *http.Request {
				return bearerRequest(t, http.MethodGet, "/v1/products/"+s.otherID, s.accessToken)
			}},
		isolationCase{route: route{"PATCH", "/v1/products/{id}"}, seed: seedProduct,
			request: func(t *testing.T, s seeded) *http.Request {
				r := bodyRequest(t, http.MethodPatch, "/v1/products/"+s.otherID, s.accessToken, map[string]any{"title": "Mine"})
				r.Header.Set("If-Match", "1")
				return r
			}},
		isolationCase{route: route{"DELETE", "/v1/products/{id}"}, seed: seedProduct,
			request: func(t *testing.T, s seeded) *http.Request {
				return bearerRequest(t, http.MethodDelete, "/v1/products/"+s.otherID, s.accessToken)
			}},
	)
	auditCases = append(auditCases,
		auditCase{route: route{"POST", "/v1/products"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			return bodyRequest(t, http.MethodPost, "/v1/products", s.accessToken, map[string]any{"title": "Audited"}), "product", ""
		}},
		auditCase{route: route{"PATCH", "/v1/products/{id}"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			id := apiCreate(t, s, "/v1/products", map[string]any{"title": "Before"})
			r := bodyRequest(t, http.MethodPatch, "/v1/products/"+id, s.accessToken, map[string]any{"title": "After"})
			r.Header.Set("If-Match", "1")
			return r, "product", id
		}},
		auditCase{route: route{"DELETE", "/v1/products/{id}"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			id := apiCreate(t, s, "/v1/products", map[string]any{"title": "Doomed"})
			return bearerRequest(t, http.MethodDelete, "/v1/products/"+id, s.accessToken), "product", id
		}},
	)
}

// seedProduct is a signed-in admin plus one product whose title is the marker.
func seedProduct(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID) seeded {
	t.Helper()
	s := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	id := uuid.Must(uuid.NewV7())
	s.marker = "Product " + id.String()
	s.id = id.String()
	if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO products (id, tenant_id, title, slug) VALUES ($1, $2, $3, slugify($3))`,
			id, tenantID, s.marker)
		return err
	}); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	return s
}

// TestProducts is P1-028's acceptance (04-api-spec.md §7.1; BR-008, BR-009,
// BR-010, BR-012, BR-042).
func TestProducts(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	admin := seedSignedInUserWithRole(ctx, t, store, uuid.Must(uuid.NewV7()), auth.RoleAdmin)
	do := apiClient(t, admin)
	patch := func(id string, version any, body map[string]any) (int, map[string]any) {
		t.Helper()
		return doWithHeaders(t, admin, http.MethodPatch, "/v1/products/"+id, body, map[string]string{"If-Match": fmt.Sprint(version)})
	}

	_, brand := do("POST", "/v1/brands", map[string]any{"name": "Erigo"})
	_, tees := do("POST", "/v1/categories", map[string]any{"name": "Tees"})
	_, ss26 := do("POST", "/v1/categories", map[string]any{"name": "SS26", "kind": "series"})

	code, p := do("POST", "/v1/products", map[string]any{
		"title": "Erigo Basic Tee", "brand_id": brand["id"],
		"category_ids": []any{tees["id"], ss26["id"]}, "attributes": map[string]any{"material": "Cotton"}})
	if code != 201 || p["slug"] != "erigo-basic-tee" || p["status"] != "draft" || p["version"] != float64(1) ||
		p["brand"].(map[string]any)["name"] != "Erigo" || len(p["categories"].([]any)) != 2 || p["description"] != nil {
		t.Fatalf("create: %d %v", code, p)
	}
	id := p["id"].(string)

	t.Run("a colliding slug gets -2, archived products included", func(t *testing.T) {
		code, q := do("POST", "/v1/products", map[string]any{"title": "Erigo Basic Tee!"})
		if code != 201 || q["slug"] != "erigo-basic-tee-2" {
			t.Fatalf("second: %d %v", code, q)
		}
		do("DELETE", "/v1/products/"+q["id"].(string), nil)
		code, q = do("POST", "/v1/products", map[string]any{"title": "erigo basic tee"})
		if code != 201 || q["slug"] != "erigo-basic-tee-3" {
			t.Errorf("third: %d %v", code, q)
		}
	})
	t.Run("server-managed and matrix-only fields are refused", func(t *testing.T) {
		for _, f := range []string{"slug", "status", "version", "option_names"} {
			code, q := do("POST", "/v1/products", map[string]any{"title": "X", f: "x"})
			assertProblem(t, code, q, 422, "validation_failed", f)
		}
		code, q := patch(id, 1, map[string]any{"option_names": []any{"Size"}})
		assertProblem(t, code, q, 422, "validation_failed", "option_names")
	})
	t.Run("references must be live rows of this tenant", func(t *testing.T) {
		code, q := do("POST", "/v1/products", map[string]any{"title": "X", "brand_id": uuid.NewString()})
		assertProblem(t, code, q, 422, "validation_failed", "brand_id")
		code, q = do("POST", "/v1/products", map[string]any{"title": "X", "category_ids": []any{uuid.NewString()}})
		assertProblem(t, code, q, 422, "validation_failed", "category_ids")
	})
	t.Run("If-Match is required and checked", func(t *testing.T) {
		code, q := do("PATCH", "/v1/products/"+id, map[string]any{"title": "X"})
		assertProblem(t, code, q, 422, "validation_failed", "If-Match")
		code, q = patch(id, 7, map[string]any{"title": "X"})
		assertProblem(t, code, q, 409, "version_conflict", "version")
	})
	t.Run("a title change never changes the slug; nulls clear", func(t *testing.T) {
		code, q := patch(id, 1, map[string]any{"title": "Erigo Basic Tee v2", "description": "Kaos 30s"})
		if code != 200 || q["slug"] != "erigo-basic-tee" || q["version"] != float64(2) || q["description"] != "Kaos 30s" {
			t.Fatalf("title: %d %v", code, q)
		}
		code, q = patch(id, 2, map[string]any{"description": nil, "brand_id": nil, "category_ids": []any{tees["id"]}})
		if code != 200 || q["description"] != nil || q["brand"] != nil || len(q["categories"].([]any)) != 1 {
			t.Fatalf("clear: %d %v", code, q)
		}
		code, q = patch(id, 3, map[string]any{"title": nil})
		assertProblem(t, code, q, 422, "validation_failed", "title")
	})
	t.Run("slug is editable, validated and unique", func(t *testing.T) {
		code, q := patch(id, 3, map[string]any{"slug": "basic-tee"})
		if code != 200 || q["slug"] != "basic-tee" {
			t.Fatalf("slug: %d %v", code, q)
		}
		code, q = patch(id, 4, map[string]any{"slug": "Basic Tee"})
		assertProblem(t, code, q, 422, "validation_failed", "slug")
		code, q = patch(id, 4, map[string]any{"slug": "erigo-basic-tee-2"})
		assertProblem(t, code, q, 422, "validation_failed", "slug")
	})
	t.Run("archive is DELETE, not a status", func(t *testing.T) {
		code, q := patch(id, 4, map[string]any{"status": "archived"})
		assertProblem(t, code, q, 422, "validation_failed", "status")
		if code, _ := do("DELETE", "/v1/products/"+id, nil); code != 204 {
			t.Fatalf("delete: %d", code)
		}
		code, q = do("GET", "/v1/products/"+id, nil)
		if code != 200 || q["status"] != "archived" || q["archived_at"] == nil {
			t.Errorf("after archive: %d %v", code, q)
		}
	})
	t.Run("viewer reads, cannot write", func(t *testing.T) {
		viewer := seedSignedInUserWithRole(ctx, t, store, uuid.Must(uuid.NewV7()), auth.RoleViewer)
		code, q := apiClient(t, viewer)("POST", "/v1/products", map[string]any{"title": "X"})
		assertProblem(t, code, q, 403, "permission_denied", "")
	})
}
