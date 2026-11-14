package storefront

import (
	"testing"
)

// TestStorefrontPackages is P1-934's storefront half (BR-171): a package's
// whole details on its page, its card summary in the list, and null for a
// product without one.
func TestStorefrontPackages(t *testing.T) {
	store := openStore(t)
	tenantID, key := seedKey(t, store, "https://umrah.example")
	seedCatalog(t, store, tenantID)
	execTenant(t, store, tenantID,
		`INSERT INTO products (id, tenant_id, title, slug, status, option_names, package)
		   VALUES ('{ida003}', `+tid+`, 'Umrah Reguler', 'umrah-reguler', 'active', '{"Kamar"}',
		   '{"departure_date":"2026-12-14","departure_text":null,"return_date":null,"duration_days":9,
		     "airline":"Saudia","route":null,"hotels":[{"city":"Makkah","name":"Hilton","stars":4,"nights":5,"variant":null}],
		     "seats_left":3,"seats_total":45,"itinerary":[{"title":"Jakarta – Madinah","text":null}],
		     "includes":["Visa"],"excludes":[],"bonus":[],"price_note":null,"cancellation_policy":null}')`,
		`INSERT INTO variants (id, tenant_id, product_id, option_values, regular_price_amount)
		   VALUES (gen_random_uuid(), `+tid+`, '{ida003}', '{"Quad"}', 3190000000)`,
		`INSERT INTO variants (id, tenant_id, product_id, option_values, regular_price_amount, price_usd)
		   VALUES (gen_random_uuid(), `+tid+`, '{ida003}', '{"Double"}', 0, 1850000)`)
	s := newShop(t, store, key)

	code, body, raw := s.get(t, "/v1/storefront/products")
	if code != 200 {
		t.Fatalf("list: %d %s", code, raw)
	}
	for _, row := range body["data"].([]any) {
		p := row.(map[string]any)
		switch p["slug"] {
		case "umrah-reguler":
			pkg := p["package"].(map[string]any)
			if pkg["departure_date"] != "2026-12-14" || pkg["seats_left"] != float64(3) || pkg["airline"] != "Saudia" ||
				len(pkg["hotels"].([]any)) != 1 || pkg["itinerary"] != nil || len(pkg) != 8 {
				t.Errorf("card summary: %v", pkg)
			}
			// P1-935: the lowest USD price, while the rupiah ones ignore it.
			if p["price_usd_min"] != float64(1850000) || p["price_max"] != float64(3190000000) {
				t.Errorf("prices: usd %v, rupiah max %v", p["price_usd_min"], p["price_max"])
			}
		default:
			if v, ok := p["package"]; !ok || v != nil || p["price_usd_min"] != nil {
				t.Errorf("%s: package %v, price_usd_min %v, want null", p["slug"], v, p["price_usd_min"])
			}
		}
	}

	code, detail, raw := s.get(t, "/v1/storefront/products/umrah-reguler")
	pkg, _ := detail["package"].(map[string]any)
	if code != 200 || len(pkg) != 15 || pkg["route"] != nil || len(pkg["itinerary"].([]any)) != 1 {
		t.Fatalf("detail: %d %s", code, raw)
	}
	usd := 0
	for _, v := range detail["variants"].([]any) {
		if v.(map[string]any)["price_usd"] == float64(1850000) {
			usd++
		}
	}
	if usd != 1 {
		t.Errorf("variants' USD prices: %s", raw)
	}
	if _, tee, _ := s.get(t, "/v1/storefront/products/basic-tee"); tee["package"] != nil {
		t.Errorf("a retail product's package: %v", tee["package"])
	}
}
