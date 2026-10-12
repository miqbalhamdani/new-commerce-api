package storefront

import (
	"testing"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
)

// shipsFrom gives a shop an origin and two couriers (BR-120), and the seeded
// tees a weight.
func shipsFrom(t *testing.T, store *db.Store, tenantID uuid.UUID) {
	t.Helper()
	execTenant(t, store, tenantID,
		`INSERT INTO storefront_settings (tenant_id, midtrans_webhook_id, origin_postal_code, shipping_couriers)
		 VALUES (current_setting('app.tenant_id')::uuid, gen_random_uuid(), '40115', '{jne,sicepat}')
		 ON CONFLICT (tenant_id) DO UPDATE SET origin_postal_code = '40115', shipping_couriers = '{jne,sicepat}'`,
		`UPDATE variants SET weight_grams = 200`)
}

// TestCartShippingRates is P1-218's storefront half (04-api-spec.md §11.6;
// BR-120, BR-121).
func TestCartShippingRates(t *testing.T) {
	store := openStore(t)
	tenantID, key := seedKey(t, store, "https://a.example")
	seedCatalog(t, store, tenantID)
	a := newShop(t, store, key)
	sale := variantBySKU(t, store, tenantID, "TS-BLK-S")
	cart := cartWith(t, a, map[string]int{sale: 2})
	path := "/v1/storefront/carts/" + cart + "/shipping-rates"
	to := map[string]any{"destination_postal_code": "60231"}

	code, q, raw := a.do(t, "POST", path, to)
	if code != 200 || len(q["options"].([]any)) != 0 {
		t.Fatalf("a shop with no origin: %d %s, want no options", code, raw)
	}

	shipsFrom(t, store, tenantID)
	code, q, raw = a.do(t, "POST", path, to)
	opts, _ := q["options"].([]any)
	if code != 200 || q["weight_grams"] != float64(400) || len(opts) != 2 {
		t.Fatalf("quote: %d %s", code, raw)
	}
	if o := opts[0].(map[string]any); o["price"] != float64(1100000) || o["courier_service_code"] != "reg" || o["duration"] != "2 - 3 days" {
		t.Errorf("option %v", o)
	}

	// Within the window the same quote comes back, even if Biteship's price
	// moved: checkout will charge what was shown (BR-121).
	asks := a.rates.asks
	a.rates.set(1300000, false)
	if _, q, _ = a.do(t, "POST", path, to); a.rates.asks != asks || q["options"].([]any)[0].(map[string]any)["price"] != float64(1100000) {
		t.Errorf("second quote: %d asks (was %d), %v", a.rates.asks, asks, q)
	}

	a.rates.set(1100000, true)
	code, body, _ := a.do(t, "POST", path, map[string]any{"destination_postal_code": "10110"})
	assertProblem(t, "Biteship down", code, body, 502, "shipping_rates_unavailable", "")
	a.rates.set(1100000, false)

	code, body, _ = a.do(t, "POST", path, map[string]any{"destination_postal_code": "601"})
	assertProblem(t, "a bad postal code", code, body, 422, "validation_failed", "destination_postal_code")
	code, body, _ = a.do(t, "POST", "/v1/storefront/carts/"+cartWith(t, a, nil)+"/shipping-rates", to)
	assertProblem(t, "an empty cart", code, body, 422, "empty_cart", "")
	code, body, _ = a.do(t, "POST", "/v1/storefront/carts/"+uuid.NewString()+"/shipping-rates", to)
	assertProblem(t, "an unknown cart", code, body, 404, "not_found", "")
	code, body, _ = a.do(t, "POST", path, map[string]any{"destination_postal_code": "60231", "price": 0})
	assertProblem(t, "a price (BR-089)", code, body, 422, "unknown_field", "price")
}
