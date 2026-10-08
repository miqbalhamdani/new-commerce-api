package httpapi_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
)

func init() {
	isolationCases = append(isolationCases, isolationCase{
		route: route{"POST", "/v1/products/bulk"}, seed: seedVariant,
		request: func(t *testing.T, s seeded) *http.Request {
			return bodyRequest(t, http.MethodPost, "/v1/products/bulk", s.accessToken, map[string]any{
				"on_conflict": "update", "items": []any{map[string]any{"sku": "NEW-" + uuid.NewString(), "title": "New"}}})
		}})
	auditCases = append(auditCases, auditCase{
		route: route{"POST", "/v1/products/bulk"},
		request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			return bodyRequest(t, http.MethodPost, "/v1/products/bulk", s.accessToken, map[string]any{
				"on_conflict": "update", "items": []any{map[string]any{"title": "Bulk tee"}}}), "products", "bulk"
		}})
}

// TestBulk is P1-072's acceptance: up to 500 rows, per-row results by
// index, one bad row rolls back nothing (BR-043).
func TestBulk(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	admin := seedSignedInUserWithRole(ctx, t, store, uuid.Must(uuid.NewV7()), auth.RoleAdmin)
	do := apiClient(t, admin)

	existing := apiCreate(t, admin, "/v1/products", map[string]any{"title": "Erigo Basic Tee"})
	apiCreate(t, admin, "/v1/products/"+existing+"/variants", map[string]any{"option_values": []string{},
		"sku": "TS-BLK-S", "regular_price": 19900000, "weight_grams": 200})

	code, r := do("POST", "/v1/products/bulk", map[string]any{"on_conflict": "update", "items": []any{
		map[string]any{"sku": "TS-BLK-S", "regular_price": 21900000},                  // 0 updated
		map[string]any{"sku": "NEW-1", "title": "New Tee", "regular_price": 10000000}, // 1 created
		map[string]any{"title": "No SKU Tee"},                                         // 2 created
		map[string]any{"sku": "NEW-2"},                                                // 3 no title
		map[string]any{"sku": "TS-BLK-S", "sale_price": 99999999},                     // 4 sale above regular
		map[string]any{"sku": "NEW-3", "title": "Draft Only", "status": "active"},     // 5 fails publish
	}})
	if code != 200 || r["created"] != float64(2) || r["updated"] != float64(1) || r["failed"] != float64(3) {
		t.Fatalf("%d %v", code, r)
	}
	results := r["results"].([]any)
	for i, want := range []struct{ status, code string }{
		{"updated", ""}, {"created", ""}, {"created", ""}, {"error", "validation_failed"},
		{"error", "validation_failed"}, {"error", "publish_check_failed"},
	} {
		got := results[i].(map[string]any)
		if got["index"] != float64(i) || got["status"] != want.status || (want.code != "" && got["code"] != want.code) {
			t.Errorf("row %d: %v, want %v", i, got, want)
		}
	}
	_, list := do("GET", "/v1/products?q=TS-BLK-S", nil)
	if p := list["data"].([]any)[0].(map[string]any); p["price_min"] != float64(21900000) {
		t.Errorf("the update before the failed rows did not stay applied: %v", p)
	}
	if _, l := do("GET", "/v1/products?q=Draft+Only", nil); len(l["data"].([]any)) != 0 {
		t.Error("a row that failed its publish check left its product behind")
	}

	t.Run("on_conflict error reports the holder", func(t *testing.T) {
		_, r := do("POST", "/v1/products/bulk", map[string]any{"on_conflict": "error",
			"items": []any{map[string]any{"sku": "TS-BLK-S", "title": "X"}}})
		row := r["results"].([]any)[0].(map[string]any)
		if row["code"] != "duplicate_sku" || row["detail"] != "SKU TS-BLK-S is used by Erigo Basic Tee." {
			t.Errorf("%v", row)
		}
	})
	t.Run("over 500 rows is 422", func(t *testing.T) {
		items := make([]any, 501)
		for i := range items {
			items[i] = map[string]any{"title": "x"}
		}
		code, p := do("POST", "/v1/products/bulk", map[string]any{"on_conflict": "update", "items": items})
		assertProblem(t, code, p, 422, "validation_failed", "items")
	})
	t.Run("needs products:write and variants:write", func(t *testing.T) {
		ops := seedSignedInUserWithRole(ctx, t, store, uuid.Must(uuid.NewV7()), auth.RoleOps)
		code, p := apiClient(t, ops)("POST", "/v1/products/bulk", map[string]any{"on_conflict": "update", "items": []any{}})
		assertProblem(t, code, p, 403, "permission_denied", "")
	})
}
