package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

func init() {
	isolationCases = append(isolationCases,
		isolationCase{route: route{"GET", "/v1/orders"}, seed: seedOrderAt("pending"),
			request: func(t *testing.T, s seeded) *http.Request {
				return bearerRequest(t, http.MethodGet, "/v1/orders?limit=200", s.accessToken)
			}})
}

// TestOrderList is P1-103's behaviour: the saved-view filters, q over the
// number and the customer snapshot, WIB date bounds, both sorts, cursor
// paging and the aggregates (04-api-spec.md §5.1; BR-007, BR-075).
func TestOrderList(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	tenantID := uuid.Must(uuid.NewV7())
	admin := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	do := apiClient(t, admin)

	pending, _ := insertOrder(ctx, t, store, tenantID, "pending", "Rina Wati")
	paid, _ := insertOrder(ctx, t, store, tenantID, "paid", "Dewi")
	processing, _ := insertOrder(ctx, t, store, tenantID, "processing", "Sari")
	shipped, _ := insertOrder(ctx, t, store, tenantID, "shipped", "Putri")
	owed, _ := insertOrder(ctx, t, store, tenantID, "refundable", "Ayu")
	cancelledUnpaid, _ := insertOrder(ctx, t, store, tenantID, "cancelled", "Lia")

	// One order gains a full snapshot, a storefront source and a fixed
	// placed_at, for the q, source and date filters.
	if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE orders SET
			customer = jsonb_build_object('name', 'Budi', 'email', 'budi@example.com', 'phone', '+628111222333'),
			source = 'storefront', placed_at = '2026-01-15 10:00+07' WHERE id = $1`, shipped)
		return err
	}); err != nil {
		t.Fatalf("decorate order: %v", err)
	}

	ids := func(path string) []string {
		t.Helper()
		code, p := do("GET", path, nil)
		if code != 200 {
			t.Fatalf("%s: %d %v", path, code, p)
		}
		var out []string
		for _, d := range p["data"].([]any) {
			out = append(out, d.(map[string]any)["id"].(string))
		}
		slices.Sort(out)
		return out
	}
	want := func(want ...string) []string { slices.Sort(want); return want }

	for path, exp := range map[string][]string{
		"/v1/orders?status=pending":                              want(pending),
		"/v1/orders?status=paid&status=processing":               want(paid, processing),
		"/v1/orders?status=shipped":                              want(shipped),
		"/v1/orders?refund_owed=true":                            want(owed),
		"/v1/orders?refund_owed=false":                           want(pending, paid, processing, shipped, cancelledUnpaid),
		"/v1/orders?source=storefront":                           want(shipped),
		"/v1/orders?q=rina":                                      want(pending),
		"/v1/orders?q=budi%40example.com":                        want(shipped),
		"/v1/orders?q=8111222":                                   want(shipped),
		"/v1/orders?customer_id=" + uuid.NewString():             nil,
		"/v1/orders?placed_from=2026-01-01&placed_to=2026-02-01": want(shipped),
		"/v1/orders?placed_from=2026-01-15":                      want(pending, paid, processing, shipped, owed, cancelledUnpaid),
		"/v1/orders?placed_from=2026-01-16&placed_to=2026-01-17": nil,
	} {
		if got := ids(path); !slices.Equal(got, exp) {
			t.Errorf("%s: %v, want %v", path, got, exp)
		}
	}

	t.Run("q matches the order number", func(t *testing.T) {
		code, p := do("GET", "/v1/orders?status=paid", nil)
		if code != 200 {
			t.Fatalf("list: %d", code)
		}
		number := p["data"].([]any)[0].(map[string]any)["order_number"].(string)
		if got := ids("/v1/orders?q=" + number); !slices.Equal(got, want(paid)) {
			t.Errorf("q=%s: %v, want %v", number, got, want(paid))
		}
	})

	t.Run("a timestamp filter without an offset is 422", func(t *testing.T) {
		code, p := do("GET", "/v1/orders?placed_from=2026-01-15T10:00:00", nil)
		assertProblem(t, code, p, 422, "validation_failed", "placed_from")
	})

	t.Run("item_count sums qty and total is the stored amount", func(t *testing.T) {
		product := apiCreate(t, admin, "/v1/products", map[string]any{"title": "List Tee"})
		variant := apiCreate(t, admin, "/v1/products/"+product+"/variants",
			map[string]any{"option_values": []string{}, "sku": "LIST-1", "regular_price": 5000000})
		if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
			for _, qty := range []int{2, 3} {
				if _, err := tx.Exec(ctx, `INSERT INTO order_lines (id, tenant_id, order_id, variant_id, sku_snapshot, title_snapshot, qty, unit_price)
					VALUES ($1, $2, $3, $4, 'LIST-1', 'List Tee', $5, 5000000)`,
					uuid.Must(uuid.NewV7()), tenantID, paid, variant, qty); err != nil {
					return err
				}
			}
			_, err := tx.Exec(ctx, `UPDATE orders SET subtotal_amount = 25000000, total_amount = 25000000 WHERE id = $1`, paid)
			return err
		}); err != nil {
			t.Fatalf("lines: %v", err)
		}
		code, p := do("GET", "/v1/orders?status=paid", nil)
		if code != 200 {
			t.Fatalf("list: %d", code)
		}
		row := p["data"].([]any)[0].(map[string]any)
		if row["item_count"] != float64(5) || row["total"] != float64(25000000) || !strings.HasPrefix(row["customer"].(map[string]any)["name"].(string), "Dewi") {
			t.Errorf("row %v", row)
		}
	})

	t.Run("both sorts and cursor pages cover every row once", func(t *testing.T) {
		for _, sort := range []string{"-placed_at", "placed_at"} {
			var seen []string
			path := "/v1/orders?limit=2&sort=" + sort
			for i := 0; path != "" && i < 10; i++ {
				_, p := do("GET", path, nil)
				for _, d := range p["data"].([]any) {
					seen = append(seen, d.(map[string]any)["id"].(string))
				}
				path = ""
				if c, ok := p["next_cursor"].(string); ok {
					path = "/v1/orders?limit=2&sort=" + sort + "&cursor=" + c
				}
			}
			if len(seen) != 6 || len(slices.Compact(slices.Sorted(slices.Values(seen)))) != 6 {
				t.Errorf("sort %s: %v", sort, seen)
			}
		}
	})

	t.Run("limit=0 is clamped, not a panic", func(t *testing.T) {
		code, p := do("GET", "/v1/orders?limit=0", nil)
		if code != 200 || len(p["data"].([]any)) != 1 {
			t.Errorf("limit=0: %d %v", code, p)
		}
	})

	t.Run("a viewer can read the list", func(t *testing.T) {
		viewer := signInAnotherUser(ctx, t, store, tenantID, auth.RoleViewer)
		code, _ := doWithHeaders(t, viewer, http.MethodGet, "/v1/orders", nil, nil)
		if code != 200 {
			t.Errorf("viewer list: %d", code)
		}
	})
}

// TestOrderListP95 is P1-103's acceptance: p95 first byte under 800 ms with
// 10,000 orders in the tenant.
func TestOrderListP95(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds 10k orders")
	}
	ctx := t.Context()
	store := openAppStore(ctx, t)
	tenantID := uuid.Must(uuid.NewV7())
	admin := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	tctx := tenant.NewContext(ctx, tenantID)

	variantID := uuid.Must(uuid.NewV7())
	if err := store.InTenantTx(tctx, func(tx pgx.Tx) error {
		productID := uuid.Must(uuid.NewV7())
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO products (id, tenant_id, title, slug) VALUES ($1, current_setting('app.tenant_id')::uuid, 'Perf Tee', 'perf-tee')`, []any{productID}},
			{`INSERT INTO variants (id, tenant_id, product_id, sku, regular_price_amount) VALUES ($1, current_setting('app.tenant_id')::uuid, $2, 'PERF-1', 10000000)`, []any{variantID, productID}},
			{`INSERT INTO orders (id, tenant_id, source, order_number, status, customer, placed_at, paid_at,
			                      cancelled_at, courier, tracking_number, subtotal_amount, total_amount)
			   SELECT gen_random_uuid(), current_setting('app.tenant_id')::uuid,
			          CASE WHEN g % 4 = 0 THEN 'storefront' ELSE 'manual' END,
			          'P-' || g,
			          (ARRAY['pending','paid','processing','shipped','completed','cancelled'])[1 + g % 6],
			          jsonb_build_object('name', 'Customer ' || g, 'email', 'c' || g || '@example.com', 'phone', '+628' || g),
			          now() - (g || ' minutes')::interval,
			          CASE WHEN g % 6 IN (1, 2, 3, 4) OR (g % 6 = 5 AND g % 2 = 0) THEN now() - (g || ' minutes')::interval END,
			          CASE WHEN g % 6 = 5 THEN now() END,
			          CASE WHEN g % 6 IN (3, 4) THEN 'jne' END,
			          CASE WHEN g % 6 IN (3, 4) THEN 'TRK' || g END,
			          10000000, 10000000
			     FROM generate_series(1, 10000) g`, nil},
			{`INSERT INTO order_lines (id, tenant_id, order_id, variant_id, sku_snapshot, title_snapshot, qty, unit_price)
			   SELECT gen_random_uuid(), o.tenant_id, o.id, $1, 'PERF-1', 'Perf Tee', 1 + s, 10000000
			     FROM orders o, generate_series(0, 1) s
			    WHERE o.order_number LIKE 'P-%'`, []any{variantID}},
		} {
			if _, err := tx.Exec(ctx, q.sql, q.args...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed 10k: %v", err)
	}
	t.Cleanup(func() {
		bg := tenant.NewContext(t.Context(), tenantID)
		_ = store.InTenantTx(bg, func(tx pgx.Tx) error {
			_, err := tx.Exec(bg, `DELETE FROM orders; DELETE FROM products`)
			return err
		})
	})

	owner, err := pgx.Connect(ctx, config.DatabaseURL())
	if err != nil {
		t.Fatalf("connect as owner: %v", err)
	}
	defer func() { _ = owner.Close(ctx) }()
	if _, err := owner.Exec(ctx, `ANALYZE orders; ANALYZE order_lines`); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	srv := newServer(t)
	queries := []string{
		"/v1/orders", "/v1/orders?status=pending", "/v1/orders?status=paid&status=processing",
		"/v1/orders?refund_owed=true", "/v1/orders?q=Customer+5000", "/v1/orders?q=P-7500",
		"/v1/orders?source=storefront", "/v1/orders?limit=200", "/v1/orders?sort=placed_at",
	}
	var took []time.Duration
	slowest := map[string]time.Duration{}
	for i := 0; i < 40; i++ {
		start := time.Now()
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, bearerRequest(t, http.MethodGet, queries[i%len(queries)], admin.accessToken))
		took = append(took, time.Since(start))
		slowest[queries[i%len(queries)]] = max(slowest[queries[i%len(queries)]], time.Since(start))
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", queries[i%len(queries)], rec.Code, rec.Body)
		}
	}
	slices.Sort(took)
	p95 := took[len(took)*95/100]
	t.Logf("p95 %s over %d requests at 10k orders; slowest per query: %v", p95, len(took), slowest)
	if p95 > 800*time.Millisecond {
		t.Errorf("p95 %s, want under 800ms (P1-103)", p95)
	}
}
