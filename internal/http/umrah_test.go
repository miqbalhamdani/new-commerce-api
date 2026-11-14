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

// umrahOwner is a signed-in owner of a new umrah shop (BR-170).
func umrahOwner(ctx context.Context, t *testing.T, store *db.Store) seeded {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	owner := seedSignedInUserWithRole(ctx, t, store, id, auth.RoleOwner)
	tctx := tenant.NewContext(ctx, id)
	if err := store.InTenantTx(tctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(tctx, `UPDATE tenants SET business_type = 'umrah' WHERE id = $1`, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return signInAgain(ctx, t, store, owner)
}

// TestPackages is P1-934's acceptance (BR-171): package details on an umrah
// shop's products, checked, replaced whole, and refused in a retail shop.
func TestPackages(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	retailOwner := seedSignedInUserWithRole(ctx, t, store, uuid.Must(uuid.NewV7()), auth.RoleOwner)
	umrahShop := umrahOwner(ctx, t, store)
	retail, umrah := apiClient(t, retailOwner), apiClient(t, umrahShop)
	patch := func(s seeded, id string, version int, body map[string]any) (int, map[string]any) {
		t.Helper()
		return doWithHeaders(t, s, http.MethodPatch, "/v1/products/"+id, body, map[string]string{"If-Match": fmt.Sprint(version)})
	}
	pkg := map[string]any{"departure_date": "2026-12-14", "seats_left": 12, "seats_total": 45,
		"hotels":    []any{map[string]any{"city": "Makkah", "name": "Hilton Convention", "stars": 4}},
		"itinerary": []any{map[string]any{"title": "Jakarta – Madinah", "text": "Berangkat"}}}

	t.Run("a retail shop cannot send package", func(t *testing.T) {
		code, p := retail("POST", "/v1/products", map[string]any{"title": "Tee", "package": pkg})
		assertProblem(t, code, p, http.StatusUnprocessableEntity, "validation_failed", "package")
		code, p = retail("POST", "/v1/products", map[string]any{"title": "Tee"})
		if code != http.StatusCreated || p["package"] != nil {
			t.Fatalf("retail product: %d %v", code, p)
		}
		code, p = patch(retailOwner, p["id"].(string), 1, map[string]any{"package": nil})
		assertProblem(t, code, p, http.StatusUnprocessableEntity, "validation_failed", "package")
	})

	t.Run("an umrah shop's package: created, checked, replaced and cleared", func(t *testing.T) {
		code, p := umrah("POST", "/v1/products", map[string]any{"title": "Umrah Reguler 9 Hari", "package": pkg})
		if code != http.StatusCreated {
			t.Fatalf("create: %d %v", code, p)
		}
		got := p["package"].(map[string]any)
		if got["departure_date"] != "2026-12-14" || got["seats_left"] != float64(12) || got["route"] != nil ||
			len(got["includes"].([]any)) != 0 || len(got["hotels"].([]any)) != 1 {
			t.Errorf("package as read: %v", got)
		}
		id := p["id"].(string)
		code, p = patch(umrahShop, id, 1, map[string]any{"package": map[string]any{"seats_left": 50, "seats_total": 45}})
		assertProblem(t, code, p, http.StatusUnprocessableEntity, "validation_failed", "package.seats_left")
		code, p = patch(umrahShop, id, 1, map[string]any{"package": map[string]any{"departure_text": "Desember 2026"}})
		if code != http.StatusOK || p["package"].(map[string]any)["departure_date"] != nil {
			t.Fatalf("replace whole: %d %v", code, p)
		}
		code, list := umrah("GET", "/v1/products", nil)
		brief := list["data"].([]any)[0].(map[string]any)["package"].(map[string]any)
		if code != http.StatusOK || brief["departure_text"] != "Desember 2026" || len(brief) != 4 {
			t.Errorf("list brief: %d %v", code, brief)
		}
		code, p = patch(umrahShop, id, 2, map[string]any{"package": nil})
		if code != http.StatusOK || p["package"] != nil {
			t.Errorf("clear: %d %v", code, p)
		}
	})
}

// TestUSDPrices is P1-935's acceptance (BR-171): an umrah shop's variants
// carry a USD price, display only; a retail shop cannot send one.
func TestUSDPrices(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	retailOwner := seedSignedInUserWithRole(ctx, t, store, uuid.Must(uuid.NewV7()), auth.RoleOwner)
	umrahShop := umrahOwner(ctx, t, store)
	retail, umrah := apiClient(t, retailOwner), apiClient(t, umrahShop)
	newProduct := func(do func(string, string, any) (int, map[string]any)) string {
		t.Helper()
		code, p := do("POST", "/v1/products", map[string]any{"title": "Haji Furoda"})
		if code != http.StatusCreated {
			t.Fatalf("product: %d %v", code, p)
		}
		return p["id"].(string)
	}

	t.Run("a retail shop cannot send price_usd", func(t *testing.T) {
		id := newProduct(retail)
		code, p := retail("POST", "/v1/products/"+id+"/variants", map[string]any{"option_values": []any{}, "price_usd": 1850000})
		assertProblem(t, code, p, http.StatusUnprocessableEntity, "validation_failed", "price_usd")
		code, p = doWithHeaders(t, retailOwner, http.MethodPut, "/v1/products/"+id+"/variant-matrix", map[string]any{
			"option_names": []any{}, "archive_missing": true,
			"rows": []any{map[string]any{"option_values": []any{}, "price_usd": 1850000}}}, map[string]string{"If-Match": "1"})
		assertProblem(t, code, p, http.StatusUnprocessableEntity, "validation_failed", "price_usd")
		code, p = retail("POST", "/v1/products/bulk", map[string]any{"on_conflict": "error",
			"items": []any{map[string]any{"title": "X", "sku": "X-1", "price_usd": 100}}})
		if code != http.StatusUnprocessableEntity {
			t.Errorf("bulk with price_usd: %d %v", code, p)
		}
	})

	t.Run("an umrah shop's USD price: set, kept out of the rupiah price, cleared", func(t *testing.T) {
		id := newProduct(umrah)
		code, p := umrah("POST", "/v1/products/"+id+"/variants", map[string]any{"option_values": []any{}, "price_usd": 0})
		assertProblem(t, code, p, http.StatusUnprocessableEntity, "validation_failed", "price_usd")
		code, v := umrah("POST", "/v1/products/"+id+"/variants", map[string]any{"option_values": []any{}, "price_usd": 1850000})
		if code != http.StatusCreated || v["price_usd"] != float64(1850000) || v["price"] != float64(0) {
			t.Fatalf("create: %d %v", code, v)
		}
		code, v = doWithHeaders(t, umrahShop, http.MethodPatch, "/v1/variants/"+v["id"].(string),
			map[string]any{"price_usd": nil}, map[string]string{"If-Match": "1"})
		if code != http.StatusOK || v["price_usd"] != nil {
			t.Errorf("clear: %d %v", code, v)
		}
		code, m := doWithHeaders(t, umrahShop, http.MethodPut, "/v1/products/"+id+"/variant-matrix", map[string]any{
			"option_names": []any{}, "archive_missing": true,
			"rows": []any{map[string]any{"option_values": []any{}, "price_usd": 2000000}}}, map[string]string{"If-Match": "1"})
		if code != http.StatusOK || m["updated"] != float64(1) {
			t.Errorf("matrix: %d %v", code, m)
		}
	})
}
