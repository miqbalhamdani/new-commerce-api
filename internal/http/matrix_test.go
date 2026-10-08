package httpapi_test

import (
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
)

func init() {
	isolationCases = append(isolationCases, isolationCase{
		route: route{"PUT", "/v1/products/{id}/variant-matrix"}, seed: seedProduct,
		request: func(t *testing.T, s seeded) *http.Request {
			r := bodyRequest(t, http.MethodPut, "/v1/products/"+s.otherID+"/variant-matrix", s.accessToken,
				map[string]any{"option_names": []string{}, "rows": []any{}, "archive_missing": false})
			r.Header.Set("If-Match", "1")
			return r
		}})
	auditCases = append(auditCases, auditCase{
		route: route{"PUT", "/v1/products/{id}/variant-matrix"},
		request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			id := apiCreate(t, s, "/v1/products", map[string]any{"title": "Tee"})
			r := bodyRequest(t, http.MethodPut, "/v1/products/"+id+"/variant-matrix", s.accessToken,
				map[string]any{"option_names": []string{"Size"}, "rows": []any{map[string]any{"option_values": []string{"S"}}},
					"archive_missing": true})
			r.Header.Set("If-Match", "1")
			return r, "product", id
		}})
}

// TestVariantMatrix is P1-040's acceptance: a server-side diff in one
// request that counts created, updated, restored and archived rows (BR-040,
// BR-041).
func TestVariantMatrix(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	admin := seedSignedInUserWithRole(ctx, t, store, uuid.Must(uuid.NewV7()), auth.RoleAdmin)
	do := apiClient(t, admin)
	product := apiCreate(t, admin, "/v1/products", map[string]any{"title": "Erigo Basic Tee"})
	put := func(version int, body map[string]any) (int, map[string]any) {
		t.Helper()
		return doWithHeaders(t, admin, http.MethodPut, "/v1/products/"+product+"/variant-matrix", body,
			map[string]string{"If-Match": strconv.Itoa(version)})
	}
	grid := func(colours []string, sizes []string, price int) []any {
		var rows []any
		for _, c := range colours {
			for _, s := range sizes {
				rows = append(rows, map[string]any{"option_values": []string{c, s}, "sku": "TS-" + c + "-" + s,
					"regular_price": price, "weight_grams": 200})
			}
		}
		return rows
	}
	names := []string{"Colour", "Size"}
	sizes := []string{"S", "M", "L", "XL", "XXL"}

	code, r := put(1, map[string]any{"option_names": names, "rows": grid([]string{"Black", "White"}, sizes, 19900000), "archive_missing": true})
	if code != 200 || r["created"] != float64(10) || r["product_version"] != float64(2) || r["failed"] != float64(0) {
		t.Fatalf("2x5 grid in one request: %d %v", code, r)
	}

	t.Run("an unchanged grid is unchanged; one price edit is one update", func(t *testing.T) {
		rows := grid([]string{"Black", "White"}, sizes, 19900000)
		rows[0].(map[string]any)["regular_price"] = 21900000
		code, r := put(2, map[string]any{"option_names": names, "rows": rows, "archive_missing": true})
		if code != 200 || r["updated"] != float64(1) || r["unchanged"] != float64(9) || r["created"] != float64(0) {
			t.Fatalf("%d %v", code, r)
		}
	})
	t.Run("missing rows archive; sending one back restores it with its SKU", func(t *testing.T) {
		code, r := put(3, map[string]any{"option_names": names, "rows": grid([]string{"Black"}, sizes, 19900000), "archive_missing": true})
		if code != 200 || r["archived"] != float64(5) || len(r["archived_variant_ids"].([]any)) != 5 {
			t.Fatalf("archive: %d %v", code, r)
		}
		code, r = put(4, map[string]any{"option_names": names, "archive_missing": false,
			"rows": []any{map[string]any{"option_values": []string{"White", "S"}}}})
		if code != 200 || r["restored"] != float64(1) {
			t.Fatalf("restore: %d %v", code, r)
		}
		_, list := do("GET", "/v1/products/"+product+"/variants", nil)
		found := false
		for _, v := range list["data"].([]any) {
			if v.(map[string]any)["sku"] == "TS-White-S" {
				found = true
			}
		}
		if !found {
			t.Error("the restored variant lost its SKU")
		}
	})
	t.Run("request errors refuse everything before a write", func(t *testing.T) {
		for _, body := range []map[string]any{
			{"option_names": names, "archive_missing": false, "rows": []any{map[string]any{"option_values": []string{"Black"}}}},
			{"option_names": names, "archive_missing": false, "rows": []any{
				map[string]any{"option_values": []string{"Red", "S"}}, map[string]any{"option_values": []string{"Red", "S"}}}},
			{"option_names": []string{"Size", "Colour"}, "archive_missing": true, "rows": []any{}},
			{"option_names": []string{"Size"}, "archive_missing": false, "rows": []any{}},
		} {
			code, p := put(5, body)
			if code != 422 {
				t.Errorf("%v: %d %v", body, code, p)
			}
		}
		_, p := do("GET", "/v1/products/"+product, nil)
		if p["version"] != float64(5) {
			t.Errorf("a refused request moved the product to version %v", p["version"])
		}
	})
	t.Run("a stale version is 409", func(t *testing.T) {
		code, p := put(1, map[string]any{"option_names": names, "rows": []any{}, "archive_missing": false})
		assertProblem(t, code, p, 409, "version_conflict", "version")
	})
}

// TestVariantMatrixPartialFailure is P1-041's acceptance: one duplicate SKU
// fails only its row and the rest save; 100 cells save in under 2 s.
func TestVariantMatrixPartialFailure(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	admin := seedSignedInUserWithRole(ctx, t, store, uuid.Must(uuid.NewV7()), auth.RoleAdmin)
	other := apiCreate(t, admin, "/v1/products", map[string]any{"title": "Erigo Oversize Tee"})
	apiCreate(t, admin, "/v1/products/"+other+"/variants", map[string]any{"option_values": []string{}, "sku": "TAKEN"})
	product := apiCreate(t, admin, "/v1/products", map[string]any{"title": "Erigo Basic Tee"})

	var rows []any
	for i := 0; i < 100; i++ {
		sku := fmt.Sprintf("BIG-%03d", i)
		if i == 42 {
			sku = "TAKEN"
		}
		rows = append(rows, map[string]any{"option_values": []string{strconv.Itoa(i)}, "sku": sku,
			"regular_price": 19900000, "weight_grams": 200})
	}
	start := time.Now()
	code, r := doWithHeaders(t, admin, http.MethodPut, "/v1/products/"+product+"/variant-matrix",
		map[string]any{"option_names": []string{"Size"}, "rows": rows, "archive_missing": true}, map[string]string{"If-Match": "1"})
	took := time.Since(start)
	if code != 200 || r["created"] != float64(99) || r["failed"] != float64(1) {
		t.Fatalf("%d %v", code, r)
	}
	bad := r["results"].([]any)[42].(map[string]any)
	if bad["status"] != "error" || bad["code"] != "duplicate_sku" || bad["detail"] != "SKU TAKEN is used by Erigo Oversize Tee." {
		t.Errorf("row 42: %v", bad)
	}
	t.Logf("100 rows saved in %s", took)
	if took > 2*time.Second {
		t.Errorf("100 cells took %s, want under 2s", took)
	}
}
