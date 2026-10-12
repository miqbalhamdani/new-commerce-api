package httpapi_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
)

func init() {
	isolationCases = append(isolationCases,
		// A quotes B's variant: RLS hides it, so the line is a 422 naming it.
		isolationCase{route: route{"POST", "/v1/shipping/rates"}, seed: seedVariantForOrder,
			request: func(t *testing.T, s seeded) *http.Request {
				return bodyRequest(t, http.MethodPost, "/v1/shipping/rates", s.accessToken, map[string]any{
					"destination_postal_code": "60231", "lines": []map[string]any{{"variant_id": s.otherID, "qty": 1}}})
			}},
	)
}

// shipsFrom gives a test shop an origin and two couriers (BR-120).
func shipsFrom(t *testing.T, store *db.Store, tenantID uuid.UUID) {
	t.Helper()
	execAs(t, store, tenantID, `INSERT INTO storefront_settings (tenant_id, midtrans_webhook_id, origin_postal_code, shipping_couriers)
		VALUES (current_setting('app.tenant_id')::uuid, gen_random_uuid(), '40115', '{jne,sicepat}')`)
}

// TestManualOrderShipping is P1-218's admin half (04-api-spec.md §5.4;
// BR-120, BR-121): a quote for the lines, and a manual order charged the
// option it picked, quoted again on the server.
func TestManualOrderShipping(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	tenantID := uuid.Must(uuid.NewV7())
	admin := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	do := apiClient(t, admin)
	variant, _ := insertVariant(ctx, t, store, tenantID, "SHP")
	lines := []map[string]any{{"variant_id": variant, "qty": 2}}

	code, q := do("POST", "/v1/shipping/rates", map[string]any{"destination_postal_code": "60231", "lines": lines})
	if code != 200 || len(q["options"].([]any)) != 0 {
		t.Fatalf("a shop with no origin: %d %v, want no options", code, q)
	}
	shipsFrom(t, store, tenantID)
	code, q = do("POST", "/v1/shipping/rates", map[string]any{"destination_postal_code": "60231", "lines": lines})
	opts, _ := q["options"].([]any)
	if code != 200 || len(opts) != 2 || opts[0].(map[string]any)["price"] != float64(1000000) {
		t.Fatalf("quote: %d %v", code, q)
	}
	for what, req := range map[string]struct {
		body  map[string]any
		field string
	}{
		"a bad postal code": {map[string]any{"destination_postal_code": "601", "lines": lines}, "destination_postal_code"},
		"no lines":          {map[string]any{"destination_postal_code": "60231", "lines": []any{}}, "lines"},
	} {
		code, p := do("POST", "/v1/shipping/rates", req.body)
		t.Run(what, func(t *testing.T) { assertProblem(t, code, p, 422, "validation_failed", req.field) })
	}

	order := func(extra map[string]any) map[string]any {
		b := map[string]any{"source": "manual", "customer": map[string]any{"name": "Dewi"},
			"shipping_address": map[string]any{"line1": "Jl. Kenanga 4", "city": "Surabaya", "province": "Jawa Timur", "postal_code": "60231"},
			"lines":            lines}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	code, o := do("POST", "/v1/orders", order(map[string]any{"shipping_option": map[string]any{"courier_code": "sicepat", "courier_service_code": "reg"}}))
	if code != 201 || o["shipping"] != float64(1000000) || o["shipping_courier"] != "sicepat" || o["shipping_service"] != "reg" ||
		o["total"] != float64(2*10000000+1000000) {
		t.Fatalf("order with an option: %d %v", code, o)
	}
	code, p := do("POST", "/v1/orders", order(map[string]any{"shipping_option": map[string]any{"courier_code": "tiki", "courier_service_code": "reg"}}))
	assertProblem(t, code, p, 409, "shipping_unavailable", "shipping_option")
	code, p = do("POST", "/v1/orders", order(map[string]any{"shipping": 1500000,
		"shipping_option": map[string]any{"courier_code": "jne", "courier_service_code": "reg"}}))
	assertProblem(t, code, p, 422, "validation_failed", "shipping_option")
	code, p = do("POST", "/v1/orders", order(map[string]any{"shipping_option": map[string]any{"courier_code": "jne", "courier_service_code": "reg", "price": 1}}))
	assertProblem(t, code, p, 422, "unknown_field", "price")
}
