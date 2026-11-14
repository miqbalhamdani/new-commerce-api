package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// The duplicate route (P1-939) in the isolation and audit suites.
func init() {
	isolationCases = append(isolationCases,
		isolationCase{route: route{"POST", "/v1/products/{id}/duplicate"}, seed: seedProduct,
			request: func(t *testing.T, s seeded) *http.Request {
				return bearerRequest(t, http.MethodPost, "/v1/products/"+s.otherID+"/duplicate", s.accessToken)
			}})
	auditCases = append(auditCases,
		auditCase{route: route{"POST", "/v1/products/{id}/duplicate"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			id := apiCreate(t, s, "/v1/products", map[string]any{"title": "Original"})
			return bearerRequest(t, http.MethodPost, "/v1/products/"+id+"/duplicate", s.accessToken), "product", ""
		}})
}

// umrahOwner is a signed-in owner of a new umrah shop (BR-170), and the shop.
func umrahOwner(ctx context.Context, t *testing.T, store *db.Store) (seeded, uuid.UUID) {
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
	return signInAgain(ctx, t, store, owner), id
}

// TestPackages is P1-934's acceptance (BR-171): package details on an umrah
// shop's products, checked, replaced whole, and refused in a retail shop.
func TestPackages(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	retailOwner := seedSignedInUserWithRole(ctx, t, store, uuid.Must(uuid.NewV7()), auth.RoleOwner)
	umrahShop, _ := umrahOwner(ctx, t, store)
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
	umrahShop, _ := umrahOwner(ctx, t, store)
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

// seedImage gives a product one image, which publishing needs (BR-038).
func seedImage(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID, productID string) {
	t.Helper()
	tctx := tenant.NewContext(ctx, tenantID)
	if err := store.InTenantTx(tctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(tctx, `INSERT INTO product_media (id, tenant_id, product_id, r2_key, mime_type, bytes, position)
			VALUES (gen_random_uuid(), $1, $2, 'x/products/p/a.jpg', 'image/jpeg', 1, 0)`, tenantID, productID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// TestPublishingAPackage is P1-936's acceptance (BR-172): an umrah shop
// publishes with no weight and a USD price, and needs a departure.
func TestPublishingAPackage(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	owner, shopID := umrahOwner(ctx, t, store)
	do := apiClient(t, owner)
	_, cat := do("POST", "/v1/categories", map[string]any{"name": "Umrah"})
	code, p := do("POST", "/v1/products", map[string]any{"title": "Haji Furoda", "category_ids": []any{cat["id"]},
		"package": map[string]any{"seats_left": 3}})
	if code != http.StatusCreated {
		t.Fatalf("product: %d %v", code, p)
	}
	id := p["id"].(string)
	seedImage(ctx, t, store, shopID, id)
	if code, v := do("POST", "/v1/products/"+id+"/variants", map[string]any{"option_values": []any{}, "sku": "HF-1",
		"price_usd": 1850000}); code != http.StatusCreated {
		t.Fatalf("variant: %d %v", code, v)
	}
	publish := func(version int) (int, map[string]any) {
		return doWithHeaders(t, owner, http.MethodPatch, "/v1/products/"+id, map[string]any{"status": "active"},
			map[string]string{"If-Match": fmt.Sprint(version)})
	}
	code, p = publish(1)
	fields := map[string]bool{}
	if code == http.StatusUnprocessableEntity {
		for _, e := range p["errors"].([]any) {
			fields[e.(map[string]any)["field"].(string)] = true
		}
	}
	if !fields["package.departure"] || fields["weight"] || fields["price"] || len(fields) != 1 {
		t.Fatalf("publish without a departure: %d %v", code, p)
	}
	code, p = doWithHeaders(t, owner, http.MethodPatch, "/v1/products/"+id,
		map[string]any{"package": map[string]any{"departure_text": "Juni 2027"}}, map[string]string{"If-Match": "1"})
	if code != http.StatusOK {
		t.Fatalf("set the departure: %d %v", code, p)
	}
	if code, p = publish(2); code != http.StatusOK || p["status"] != "active" {
		t.Errorf("publish with no weight and a USD price: %d %v", code, p)
	}
}

// TestSKUOnPublish is P1-938's acceptance (BR-038, BR-039): publishing gives
// each live variant without a SKU one, past any the shop already holds, and
// leaves a SKU someone set alone.
func TestSKUOnPublish(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	shopID := uuid.Must(uuid.NewV7())
	owner := seedSignedInUserWithRole(ctx, t, store, shopID, auth.RoleOwner)
	do := apiClient(t, owner)
	_, cat := do("POST", "/v1/categories", map[string]any{"name": "Sprei"})
	code, p := do("POST", "/v1/products", map[string]any{"title": "Sprei Katun Café", "category_ids": []any{cat["id"]}})
	if code != http.StatusCreated {
		t.Fatalf("product: %d %v", code, p)
	}
	id := p["id"].(string)
	seedImage(ctx, t, store, shopID, id)
	code, m := doWithHeaders(t, owner, http.MethodPut, "/v1/products/"+id+"/variant-matrix", map[string]any{
		"option_names": []any{"Ukuran"}, "archive_missing": true,
		"rows": []any{
			map[string]any{"option_values": []any{"King Size"}, "regular_price": 100, "weight_grams": 900},
			map[string]any{"option_values": []any{"Queen"}, "regular_price": 100, "weight_grams": 800},
			map[string]any{"option_values": []any{"Single"}, "regular_price": 100, "weight_grams": 700, "sku": "MINE-1"},
		}}, map[string]string{"If-Match": "1"})
	if code != http.StatusOK || m["created"] != float64(3) {
		t.Fatalf("matrix: %d %v", code, m)
	}
	// Another product already holds the Queen's SKU, so it gets -2.
	_, other := do("POST", "/v1/products", map[string]any{"title": "Other"})
	if code, v := do("POST", "/v1/products/"+other["id"].(string)+"/variants", map[string]any{"option_values": []any{},
		"sku": "SPREI-KATUN-CAFE-QUEEN"}); code != http.StatusCreated {
		t.Fatalf("other: %d %v", code, v)
	}
	code, p = doWithHeaders(t, owner, http.MethodPatch, "/v1/products/"+id, map[string]any{"status": "active"},
		map[string]string{"If-Match": "2"})
	if code != http.StatusOK {
		t.Fatalf("publish: %d %v", code, p)
	}
	_, list := do("GET", "/v1/products/"+id+"/variants", nil)
	got := map[string]any{}
	for _, v := range list["data"].([]any) {
		v := v.(map[string]any)
		got[v["option_values"].([]any)[0].(string)] = v["sku"]
	}
	if got["King Size"] != "SPREI-KATUN-CAFE-KING-SIZE" || got["Queen"] != "SPREI-KATUN-CAFE-QUEEN-2" || got["Single"] != "MINE-1" {
		t.Errorf("SKUs given: %v", got)
	}
}

// TestDuplicateProduct is P1-939's acceptance (BR-047): a draft copy with the
// same details, its live variants without SKUs, and its images sharing their
// files, which survive deleting the copy's image.
func TestDuplicateProduct(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	owner, shopID := umrahOwner(ctx, t, store)
	do := apiClient(t, owner)
	_, cat := do("POST", "/v1/categories", map[string]any{"name": "Umrah"})
	code, p := do("POST", "/v1/products", map[string]any{"title": "Umrah Desember", "category_ids": []any{cat["id"]},
		"attributes": map[string]any{"Catatan": "x"}, "package": map[string]any{"departure_text": "Desember 2026", "seats_left": 9}})
	if code != http.StatusCreated {
		t.Fatalf("product: %d %v", code, p)
	}
	id := p["id"].(string)
	code, m := doWithHeaders(t, owner, http.MethodPut, "/v1/products/"+id+"/variant-matrix", map[string]any{
		"option_names": []any{"Kamar"}, "archive_missing": true,
		"rows": []any{
			map[string]any{"option_values": []any{"Quad"}, "regular_price": 3190000000, "sku": "UD-QUAD"},
			map[string]any{"option_values": []any{"Double"}, "regular_price": 0, "price_usd": 1850000},
		}}, map[string]string{"If-Match": "1"})
	if code != http.StatusOK {
		t.Fatalf("matrix: %d %v", code, m)
	}
	key := shopID.String() + "/products/" + id + "/" + strings.Repeat("cd", 32) + ".jpg"
	if err := testFiles(t).Put(ctx, key, strings.NewReader("jpeg"), 4, "image/jpeg"); err != nil {
		t.Fatal(err)
	}
	tctx := tenant.NewContext(ctx, shopID)
	if err := store.InTenantTx(tctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(tctx, `INSERT INTO product_media (id, tenant_id, product_id, variant_id, r2_key, mime_type, bytes, position)
			SELECT gen_random_uuid(), $1, $2, v.id, $3, 'image/jpeg', 4, 0 FROM variants v
			 WHERE v.product_id = $2 AND v.option_values = '{Quad}'`, shopID, id, key)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	code, c := do("POST", "/v1/products/"+id+"/duplicate", nil)
	if code != http.StatusCreated || c["title"] != "Umrah Desember (copy)" || c["slug"] != "umrah-desember-copy" ||
		c["status"] != "draft" || c["package"].(map[string]any)["seats_left"] != float64(9) ||
		len(c["categories"].([]any)) != 1 || c["attributes"].(map[string]any)["Catatan"] != "x" ||
		len(c["option_names"].([]any)) != 1 || c["variant_count"] != float64(2) {
		t.Fatalf("duplicate: %d %v", code, c)
	}
	copyID := c["id"].(string)
	_, vs := do("GET", "/v1/products/"+copyID+"/variants", nil)
	quad := ""
	for _, v := range vs["data"].([]any) {
		v := v.(map[string]any)
		if v["sku"] != nil {
			t.Errorf("a copied variant kept its SKU: %v", v)
		}
		if v["option_values"].([]any)[0] == "Double" && v["price_usd"] != float64(1850000) {
			t.Errorf("Double's USD price: %v", v)
		}
		if v["option_values"].([]any)[0] == "Quad" {
			quad = v["id"].(string)
		}
	}
	media := c["media"].([]any)
	if len(media) != 1 || !strings.HasSuffix(media[0].(map[string]any)["url"].(string), "/"+key) ||
		media[0].(map[string]any)["variant_id"] != quad {
		t.Fatalf("copied media: %v (Quad %s)", media, quad)
	}
	if code, _ := do("DELETE", "/v1/media/"+media[0].(map[string]any)["id"].(string), nil); code != http.StatusNoContent {
		t.Fatalf("delete the copy's image: %d", code)
	}
	if _, _, err := testFiles(t).Head(ctx, key); err != nil {
		t.Errorf("the original's file went with the copy's image: %v", err)
	}
	if code, _ := do("POST", "/v1/products/"+uuid.NewString()+"/duplicate", nil); code != http.StatusNotFound {
		t.Errorf("unknown product: %d", code)
	}
}
