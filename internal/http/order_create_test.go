package httpapi_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

func init() {
	isolationCases = append(isolationCases,
		// Tenant A orders tenant B's variant: RLS hides the row, the line is
		// a 422 naming it, and nothing of B appears in the response.
		isolationCase{route: route{"POST", "/v1/orders"}, seed: seedVariantForOrder,
			request: func(t *testing.T, s seeded) *http.Request {
				return bodyRequest(t, http.MethodPost, "/v1/orders", s.accessToken, map[string]any{
					"source": "manual", "customer": map[string]any{"name": "Iso"},
					"shipping_address": map[string]any{"line1": "Jl. A", "city": "Bandung",
						"province": "Jawa Barat", "postal_code": "40115"},
					"lines": []map[string]any{{"variant_id": s.otherID, "qty": 1}},
				})
			}},
	)
	auditCases = append(auditCases,
		auditCase{route: route{"POST", "/v1/orders"},
			request: func(t *testing.T, s seeded) (*http.Request, string, string) {
				ctx := t.Context()
				store := openAppStore(ctx, t)
				variant, _ := insertVariant(ctx, t, store, uuid.MustParse(s.tenantID), "AUD")
				return bodyRequest(t, http.MethodPost, "/v1/orders", s.accessToken, map[string]any{
					"source": "manual", "customer": map[string]any{"name": "Audit"},
					"shipping_address": map[string]any{"line1": "Jl. B", "city": "Jakarta",
						"province": "DKI Jakarta", "postal_code": "10110"},
					"lines": []map[string]any{{"variant_id": variant, "qty": 1}},
				}), "order", ""
			}},
	)
}

// insertVariant seeds a product with one 10,000,000-minor variant and returns
// the variant id and its SKU.
func insertVariant(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID, prefix string) (string, string) {
	t.Helper()
	productID, variantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	sku := prefix + "-" + variantID.String()[:8]
	if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO products (id, tenant_id, title, slug, option_names)
			VALUES ($1, $2, 'Entry Tee', 'entry-' || $3, '{"Color","Size"}')`, productID, tenantID, variantID.String()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO variants (id, tenant_id, product_id, sku, option_values, regular_price_amount)
			VALUES ($1, $2, $3, $4, '{"Black","M"}', 10000000)`, variantID, tenantID, productID, sku)
		return err
	}); err != nil {
		t.Fatalf("seed variant: %v", err)
	}
	return variantID.String(), sku
}

// seedVariantForOrder is a signed-in admin plus one variant whose SKU is the
// marker.
func seedVariantForOrder(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID) seeded {
	t.Helper()
	s := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	s.id, s.marker = insertVariant(ctx, t, store, tenantID, "ISOV")
	return s
}

// TestOrderCreate is P1-105's acceptance (04-api-spec.md §5.4; BR-046,
// BR-076, BR-077, BR-078).
func TestOrderCreate(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	tenantID := uuid.Must(uuid.NewV7())
	admin := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	do := apiClient(t, admin)

	variant, sku := insertVariant(ctx, t, store, tenantID, "ENT")
	base := func() map[string]any {
		return map[string]any{
			"source":   "manual",
			"customer": map[string]any{"name": "Dewi", "email": nil, "phone": "+6281299990000"},
			"shipping_address": map[string]any{"line1": "Jl. Kenanga 4", "city": "Surabaya",
				"province": "Jawa Timur", "postal_code": "60231"},
			"lines":    []map[string]any{{"variant_id": variant, "qty": 2, "discount": 2000000}},
			"shipping": 1500000,
			"note":     "Order via WhatsApp",
		}
	}

	t.Run("a manual order is priced from the catalog and snapshotted", func(t *testing.T) {
		code, o := do("POST", "/v1/orders", base())
		if code != 201 {
			t.Fatalf("create: %d %v", code, o)
		}
		if o["status"] != "pending" || o["source"] != "manual" || o["payment_method"] != "bank_transfer" ||
			o["customer_id"] != nil || o["version"] != float64(1) {
			t.Errorf("order %v", o)
		}
		if o["subtotal"] != float64(20000000) || o["discount"] != float64(2000000) ||
			o["shipping"] != float64(1500000) || o["total"] != float64(19500000) {
			t.Errorf("totals %v", o)
		}
		line := o["lines"].([]any)[0].(map[string]any)
		if line["sku"] != sku || line["title"] != "Entry Tee — Black / M" ||
			line["unit_price"] != float64(10000000) || line["qty"] != float64(2) {
			t.Errorf("line %v", line)
		}
		number := o["order_number"].(string)
		if !strings.HasPrefix(number, "TST-") || len(number) != len("TST-000001") {
			t.Errorf("order_number %q, want TST-000001 form (BR-077)", number)
		}
		if cust := o["customer"].(map[string]any); cust["name"] != "Dewi" || cust["email"] != nil {
			t.Errorf("customer snapshot %v", cust)
		}

		// A second order gets the next number; the first one works with no
		// seeded sequence row.
		code, o2 := do("POST", "/v1/orders", base())
		if code != 201 {
			t.Fatalf("second create: %d", code)
		}
		first, second := number[len(number)-6:], o2["order_number"].(string)[len(number)-6:]
		if second <= first {
			t.Errorf("numbers did not advance: %s then %s", first, second)
		}
	})

	t.Run("a unit_price anywhere in the body is an unknown field", func(t *testing.T) {
		b := base()
		b["lines"] = []map[string]any{{"variant_id": variant, "qty": 1, "unit_price": 1}}
		code, p := do("POST", "/v1/orders", b)
		assertProblem(t, code, p, 422, "unknown_field", "")
	})

	t.Run("an archived variant is refused naming the line", func(t *testing.T) {
		archived, _ := insertVariant(ctx, t, store, tenantID, "ARC")
		if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE variants SET archived_at = now() WHERE id = $1`, archived)
			return err
		}); err != nil {
			t.Fatalf("archive: %v", err)
		}
		b := base()
		b["lines"] = []map[string]any{{"variant_id": variant, "qty": 1}, {"variant_id": archived, "qty": 1}}
		code, p := do("POST", "/v1/orders", b)
		assertProblem(t, code, p, 422, "validation_failed", "lines.1.variant_id")
	})

	t.Run("a sale price is what the catalog charges", func(t *testing.T) {
		onSale, _ := insertVariant(ctx, t, store, tenantID, "SALE")
		if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE variants SET sale_price_amount = 7500000 WHERE id = $1`, onSale)
			return err
		}); err != nil {
			t.Fatalf("set sale: %v", err)
		}
		b := base()
		b["lines"] = []map[string]any{{"variant_id": onSale, "qty": 1}}
		code, o := do("POST", "/v1/orders", b)
		if code != 201 || o["subtotal"] != float64(7500000) {
			t.Errorf("sale price: %d %v", code, o["subtotal"])
		}
	})

	t.Run("source must be manual and a line is required", func(t *testing.T) {
		b := base()
		b["source"] = "storefront"
		code, p := do("POST", "/v1/orders", b)
		assertProblem(t, code, p, 422, "validation_failed", "source")

		b = base()
		b["lines"] = []map[string]any{}
		code, p = do("POST", "/v1/orders", b)
		assertProblem(t, code, p, 422, "validation_failed", "lines")

		b = base()
		b["shipping_option"] = map[string]any{"courier_code": "jne"}
		code, p = do("POST", "/v1/orders", b)
		assertProblem(t, code, p, 422, "validation_failed", "shipping_option")
	})
}
