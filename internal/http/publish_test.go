package httpapi_test

import (
	"net/http"
	"slices"
	"strconv"
	"testing"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
)

// TestPublishCheck is P1-049's acceptance: draft → active lists every
// failure, zero weight included, each with its variant id (BR-038).
func TestPublishCheck(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	admin := seedSignedInUserWithRole(ctx, t, store, uuid.Must(uuid.NewV7()), auth.RoleAdmin)
	do := apiClient(t, admin)
	product := apiCreate(t, admin, "/v1/products", map[string]any{"title": "Erigo Basic Tee"})
	version := 1
	publish := func() (int, map[string]any) {
		t.Helper()
		code, p := doWithHeaders(t, admin, http.MethodPatch, "/v1/products/"+product,
			map[string]any{"status": "active"}, map[string]string{"If-Match": strconv.Itoa(version)})
		return code, p
	}
	fields := func(p map[string]any) (out []string) {
		for _, e := range p["errors"].([]any) {
			f := e.(map[string]any)
			name := f["field"].(string)
			if f["variant_id"] != nil {
				name += "@" + f["variant_id"].(string)
			}
			out = append(out, name)
		}
		slices.Sort(out)
		return out
	}

	code, p := publish()
	assertProblem(t, code, p, 422, "publish_check_failed", "variants")
	if got := fields(p); !slices.Equal(got, []string{"categories", "media", "variants"}) {
		t.Errorf("empty product: %v", got)
	}

	variant := apiCreate(t, admin, "/v1/products/"+product+"/variants", map[string]any{"option_values": []string{}})
	code, p = publish()
	want := []string{"categories", "media", "price@" + variant, "sku@" + variant, "weight@" + variant}
	if got := fields(p); code != 422 || !slices.Equal(got, want) {
		t.Errorf("bare variant: %d %v, want %v", code, got, want)
	}
	_, after := do("GET", "/v1/products/"+product, nil)
	if after["status"] != "draft" || after["version"] != float64(1) {
		t.Errorf("a failed publish changed the product: %v", after)
	}

	if code, p := doWithHeaders(t, admin, http.MethodPatch, "/v1/variants/"+variant,
		map[string]any{"sku": "TS-1", "regular_price": 19900000, "weight_grams": 200}, map[string]string{"If-Match": "1"}); code != 200 {
		t.Fatalf("fix variant: %d %v", code, p)
	}
	mustConfirm(t, admin, product, pngBytes(t, 4, 4))
	tees := apiCreate(t, admin, "/v1/categories", map[string]any{"name": "Tees"})
	code, p = doWithHeaders(t, admin, http.MethodPatch, "/v1/products/"+product,
		map[string]any{"category_ids": []string{tees}, "status": "active"}, map[string]string{"If-Match": "1"})
	if code != 200 || p["status"] != "active" {
		t.Fatalf("publish: %d %v", code, p)
	}
}
