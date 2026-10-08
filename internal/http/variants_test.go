package httpapi_test

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// Variant routes (P1-029) in the isolation and audit suites. seedVariant's
// id is the variant; its product is the variant id's product, reached by
// listing as the other tenant.
func init() {
	isolationCases = append(isolationCases,
		isolationCase{route: route{"GET", "/v1/products/{id}/variants"}, seed: seedVariant,
			request: func(t *testing.T, s seeded) *http.Request {
				return bearerRequest(t, http.MethodGet, "/v1/products/"+s.otherProduct+"/variants", s.accessToken)
			}},
		isolationCase{route: route{"POST", "/v1/products/{id}/variants"}, seed: seedVariant,
			request: func(t *testing.T, s seeded) *http.Request {
				return bodyRequest(t, http.MethodPost, "/v1/products/"+s.otherProduct+"/variants", s.accessToken,
					map[string]any{"option_values": []string{}})
			}},
		isolationCase{route: route{"PATCH", "/v1/variants/{id}"}, seed: seedVariant,
			request: func(t *testing.T, s seeded) *http.Request {
				r := bodyRequest(t, http.MethodPatch, "/v1/variants/"+s.otherID, s.accessToken, map[string]any{"weight_grams": 1})
				r.Header.Set("If-Match", "1")
				return r
			}},
		isolationCase{route: route{"DELETE", "/v1/variants/{id}"}, seed: seedVariant,
			request: func(t *testing.T, s seeded) *http.Request {
				return bearerRequest(t, http.MethodDelete, "/v1/variants/"+s.otherID, s.accessToken)
			}},
	)
	auditCases = append(auditCases,
		auditCase{route: route{"POST", "/v1/products/{id}/variants"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			product := apiCreate(t, s, "/v1/products", map[string]any{"title": "Tee"})
			return bodyRequest(t, http.MethodPost, "/v1/products/"+product+"/variants", s.accessToken,
				map[string]any{"option_values": []string{}}), "variant", ""
		}},
		auditCase{route: route{"PATCH", "/v1/variants/{id}"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			product := apiCreate(t, s, "/v1/products", map[string]any{"title": "Tee"})
			id := apiCreate(t, s, "/v1/products/"+product+"/variants", map[string]any{"option_values": []string{}})
			r := bodyRequest(t, http.MethodPatch, "/v1/variants/"+id, s.accessToken, map[string]any{"weight_grams": 210})
			r.Header.Set("If-Match", "1")
			return r, "variant", id
		}},
		auditCase{route: route{"DELETE", "/v1/variants/{id}"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			product := apiCreate(t, s, "/v1/products", map[string]any{"title": "Tee"})
			id := apiCreate(t, s, "/v1/products/"+product+"/variants", map[string]any{"option_values": []string{}})
			return bearerRequest(t, http.MethodDelete, "/v1/variants/"+id, s.accessToken), "variant", id
		}},
	)
}

// seedVariant is a signed-in admin plus a product with one variant whose SKU
// is the marker.
func seedVariant(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID) seeded {
	t.Helper()
	s := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	product, variant := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	s.marker = "SKU-" + variant.String()
	s.id, s.product = variant.String(), product.String()
	if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO products (id, tenant_id, title, slug) VALUES ($1, $2, 'Tee', $3)`,
			product, tenantID, "tee-"+product.String()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO variants (id, tenant_id, product_id, sku) VALUES ($1, $2, $3, $4)`,
			variant, tenantID, product, s.marker)
		return err
	}); err != nil {
		t.Fatalf("seed variant: %v", err)
	}
	return s
}

// TestVariants is P1-029's acceptance (04-api-spec.md §7.2; BR-039, BR-046).
func TestVariants(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	tenantID := uuid.Must(uuid.NewV7())
	admin := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	do := apiClient(t, admin)
	patch := func(id string, version int, body map[string]any) (int, map[string]any) {
		t.Helper()
		return doWithHeaders(t, admin, http.MethodPatch, "/v1/variants/"+id, body, map[string]string{"If-Match": strconv.Itoa(version)})
	}

	product := apiCreate(t, admin, "/v1/products", map[string]any{"title": "Erigo Basic Tee"})
	other := apiCreate(t, admin, "/v1/products", map[string]any{"title": "Erigo Oversize Tee"})
	// The matrix (P1-040) is the way option names change; set them directly here.
	if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE products SET option_names = '{Colour,Size}' WHERE id = ANY($1)`, []string{product, other})
		return err
	}); err != nil {
		t.Fatalf("option names: %v", err)
	}
	vpath := "/v1/products/" + product + "/variants"

	code, v := do("POST", vpath, map[string]any{"option_values": []string{"Black", "S"}, "sku": "TS-BLK-S",
		"regular_price": 19900000, "weight_grams": 200})
	if code != 201 || v["price"] != float64(19900000) || v["on_sale"] != false || v["sale_price"] != nil || v["version"] != float64(1) {
		t.Fatalf("create: %d %v", code, v)
	}
	id := v["id"].(string)

	t.Run("option values must fit and be unique", func(t *testing.T) {
		code, p := do("POST", vpath, map[string]any{"option_values": []string{"Black"}})
		assertProblem(t, code, p, 422, "validation_failed", "option_values")
		code, p = do("POST", vpath, map[string]any{"option_values": []string{"Black", "S"}})
		assertProblem(t, code, p, 422, "validation_failed", "option_values")
		code, p = patch(id, 1, map[string]any{"option_values": []string{"White", "S"}})
		assertProblem(t, code, p, 422, "validation_failed", "option_values")
	})
	t.Run("a SKU held elsewhere is 409 naming the product", func(t *testing.T) {
		code, p := do("POST", "/v1/products/"+other+"/variants", map[string]any{"option_values": []string{"White", "S"}, "sku": "TS-BLK-S"})
		assertProblem(t, code, p, 409, "duplicate_sku", "sku")
		if d, _ := p["detail"].(string); d != "SKU TS-BLK-S is used by Erigo Basic Tee." {
			t.Errorf("detail %q", d)
		}
	})
	t.Run("price and on_sale are read-only", func(t *testing.T) {
		code, p := patch(id, 1, map[string]any{"price": 1})
		assertProblem(t, code, p, 422, "validation_failed", "price")
	})
	t.Run("a scheduled sale applies only inside its window", func(t *testing.T) {
		later := time.Now().Add(48 * time.Hour).Format(time.RFC3339)
		code, p := patch(id, 1, map[string]any{"sale_price": 14900000, "sale_starts_at": later})
		if code != 200 || p["price"] != float64(19900000) || p["on_sale"] != false || p["sale_price"] != float64(14900000) {
			t.Fatalf("future sale: %d %v", code, p)
		}
		code, p = patch(id, 2, map[string]any{"sale_starts_at": nil})
		if code != 200 || p["price"] != float64(14900000) || p["on_sale"] != true {
			t.Fatalf("running sale: %d %v", code, p)
		}
		code, p = patch(id, 3, map[string]any{"sale_price": nil})
		if code != 200 || p["price"] != float64(19900000) || p["on_sale"] != false {
			t.Fatalf("ended sale: %d %v", code, p)
		}
	})
	t.Run("a sale is a discount and runs forward", func(t *testing.T) {
		code, p := patch(id, 4, map[string]any{"sale_price": 19900000})
		assertProblem(t, code, p, 422, "validation_failed", "sale_price")
		code, p = patch(id, 4, map[string]any{"sale_starts_at": "2026-10-13T00:00:00+07:00", "sale_ends_at": "2026-10-10T00:00:00+07:00"})
		assertProblem(t, code, p, 422, "validation_failed", "sale_ends_at")
		if code, p := patch(id, 4, map[string]any{"sale_price": 14900000}); code != 200 {
			t.Fatalf("sale: %d %v", code, p)
		}
		code, p = patch(id, 5, map[string]any{"regular_price": 10000000})
		assertProblem(t, code, p, 422, "validation_failed", "sale_price")
	})
	t.Run("a stale If-Match is 409", func(t *testing.T) {
		code, p := patch(id, 1, map[string]any{"weight_grams": 1})
		assertProblem(t, code, p, 409, "version_conflict", "version")
	})
	t.Run("archived variants list only on request", func(t *testing.T) {
		if code, _ := do("DELETE", "/v1/variants/"+id, nil); code != 204 {
			t.Fatalf("archive: %d", code)
		}
		_, live := do("GET", vpath, nil)
		_, gone := do("GET", vpath+"?archived=true", nil)
		if len(live["data"].([]any)) != 0 || len(gone["data"].([]any)) != 1 {
			t.Errorf("live %v archived %v", live, gone)
		}
	})
}
