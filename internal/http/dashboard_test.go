package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

func init() {
	// P1-113, P1-115: the marker is a product title and a buyer's name, which
	// reach the answer only through top_products and top_customers -- so
	// tenant B's own sale must be in neither.
	isolationCases = append(isolationCases,
		isolationCase{route: route{"GET", "/v1/dashboard"}, seed: seedDashboardSale,
			request: func(t *testing.T, s seeded) *http.Request {
				today := time.Now().In(config.WIB)
				from, to := today.AddDate(0, 0, -1).Format(time.DateOnly), today.AddDate(0, 0, 2).Format(time.DateOnly)
				return bearerRequest(t, http.MethodGet, "/v1/dashboard?from="+from+"&to="+to, s.accessToken)
			}})
}

// seedDashboardSale is a signed-in admin plus one completed order, placed
// now, of a product whose title is the marker, to a buyer of the same name.
func seedDashboardSale(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID) seeded {
	t.Helper()
	s := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	s.marker = "Dashboard Tee " + tenantID.String()
	tee, _ := dashProduct(ctx, t, store, tenantID, s.marker)
	dashOrder(ctx, t, store, tenantID, dashSeed{status: "completed", placed: time.Now(), name: s.marker, email: "iso@example.com",
		lines: []dashLine{{tee, 1, 10000, 0}}})
	return s
}

type dashLine struct {
	variant         uuid.UUID
	qty             int
	price, discount int64
}

type dashSeed struct {
	status, source, method string // source defaults to manual, method to bank_transfer
	placed                 time.Time
	name, email, phone     string // name defaults to Buyer
	customer               *uuid.UUID
	lines                  []dashLine
}

// dashProduct seeds a product with one variant and returns the variant and
// the product.
func dashProduct(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID, title string) (variant, product uuid.UUID) {
	t.Helper()
	productID, variantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO products (id, tenant_id, title, slug, option_names)
			VALUES ($1, $2, $3, $4, '{"Size"}')`, productID, tenantID, title, "dash-"+productID.String()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO variants (id, tenant_id, product_id, sku, option_values, regular_price_amount)
			VALUES ($1, $2, $3, $4, '{"M"}', 10000)`, variantID, tenantID, productID, "DASH-"+variantID.String())
		return err
	}); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	return variantID, productID
}

// dashCategory seeds a category of kind under parent (uuid.Nil for a root)
// and files product in it.
func dashCategory(ctx context.Context, t *testing.T, store *db.Store, tenantID, parent, product uuid.UUID, kind, name string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	var parentID *uuid.UUID
	if parent != uuid.Nil {
		parentID = &parent
	}
	if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO categories (id, tenant_id, parent_id, kind, name) VALUES ($1, $2, $3, $4, $5)`,
			id, tenantID, parentID, kind, name); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO product_categories (tenant_id, product_id, category_id) VALUES ($1, $2, $3)`,
			tenantID, product, id)
		return err
	}); err != nil {
		t.Fatalf("seed category: %v", err)
	}
	return id
}

// dashOrder seeds one order at its status with its lines; the total is the
// lines' sum, so the table's total CHECK holds.
func dashOrder(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID, o dashSeed) {
	t.Helper()
	if o.source == "" {
		o.source = "manual"
	}
	if o.method == "" {
		o.method = "bank_transfer"
	}
	var subtotal, discount int64
	for _, l := range o.lines {
		subtotal += int64(l.qty) * l.price
		discount += l.discount
	}
	paid := o.status != "pending"
	if o.status == "refundable" {
		o.status = "cancelled"
	}
	var courier, tracking *string
	if o.status == "shipped" || o.status == "completed" {
		c, tn := "jne", "JNE1"
		courier, tracking = &c, &tn
	}
	id := uuid.Must(uuid.NewV7())
	if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO orders
			(id, tenant_id, source, order_number, status, placed_at, customer, payment_method,
			 subtotal_amount, discount_amount, total_amount, courier, tracking_number, paid_at, cancelled_at,
			 customer_id)
			VALUES ($1, $2, $3, $4, $5, $6,
			        jsonb_build_object('name', coalesce(nullif($15::text, ''), 'Buyer'),
			                           'email', nullif($7::text, ''), 'phone', nullif($8::text, '')), $9,
			        $10::bigint, $11::bigint, $10::bigint - $11::bigint, $12, $13, CASE WHEN $14 THEN $6::timestamptz END,
			        CASE WHEN $5 = 'cancelled' THEN $6::timestamptz END, $16)`,
			id, tenantID, o.source, fmt.Sprintf("DSH-%06d", orderSeq.Add(1)), o.status, o.placed,
			o.email, o.phone, o.method, subtotal, discount, courier, tracking, paid, o.name, o.customer); err != nil {
			return err
		}
		for _, l := range o.lines {
			if _, err := tx.Exec(ctx, `INSERT INTO order_lines
				(id, tenant_id, order_id, variant_id, sku_snapshot, title_snapshot, qty, unit_price, discount_amount)
				VALUES ($1, $2, $3, $4, 'SKU', 'Line', $5, $6, $7)`,
				uuid.Must(uuid.NewV7()), tenantID, id, l.variant, l.qty, l.price, l.discount); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed %s order: %v", o.status, err)
	}
}

// TestDashboard is P1-113's acceptance (04-api-spec.md §5.7; BR-007, BR-066):
// the figures over a week of seeded orders, worked out by hand below.
func TestDashboard(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	tenantID := uuid.Must(uuid.NewV7())
	admin := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	do := apiClient(t, admin)
	wib := func(s string) time.Time {
		at, err := time.ParseInLocation("2006-01-02 15:04", s, config.WIB)
		if err != nil {
			t.Fatal(err)
		}
		return at
	}
	tee, teeProduct := dashProduct(ctx, t, store, tenantID, "Tee")
	hat, _ := dashProduct(ctx, t, store, tenantID, "Hat")
	// Tee sits under Apparel > Tees in the main tree and in a collection,
	// which the dashboard leaves out as the product list does; Hat is in none.
	apparel := dashCategory(ctx, t, store, tenantID, uuid.Nil, teeProduct, "category", "Apparel")
	dashCategory(ctx, t, store, tenantID, apparel, teeProduct, "category", "Tees")
	dashCategory(ctx, t, store, tenantID, uuid.Nil, teeProduct, "collection", "Summer")
	for _, o := range []dashSeed{
		// The period, 1-7 September. 00:30 WIB on the 1st is still 31 August
		// in UTC: it must count, on the 1st.
		{status: "paid", placed: wib("2026-09-01 00:30"), name: "Rina", email: "Rina@Example.com",
			lines: []dashLine{{tee, 2, 10000, 0}, {hat, 1, 10000, 0}}},
		{status: "shipped", placed: wib("2026-09-03 10:00"), name: "Sari", phone: "+62811",
			lines: []dashLine{{hat, 2, 12000, 4000}}},
		{status: "processing", placed: wib("2026-09-05 12:00"), name: "Rina Wati", email: "rina@example.com",
			lines: []dashLine{{tee, 1, 10000, 0}}},
		{status: "completed", source: "storefront", method: "midtrans", placed: wib("2026-09-07 23:30"), name: "Dewi", email: "dewi@example.com",
			lines: []dashLine{{tee, 5, 10000, 0}}},
		// In the period but not counted: unpaid, or cancelled.
		{status: "pending", source: "storefront", method: "midtrans", placed: wib("2026-09-02 09:00"), email: "a@example.com",
			lines: []dashLine{{tee, 9, 10000, 0}}},
		{status: "pending", placed: wib("2026-09-02 10:00"), email: "b@example.com", lines: []dashLine{{tee, 9, 10000, 0}}},
		{status: "refundable", placed: wib("2026-09-04 10:00"), email: "c@example.com", lines: []dashLine{{hat, 9, 10000, 0}}},
		// to is exclusive: midnight WIB on the 8th is the next period.
		{status: "completed", placed: wib("2026-09-08 00:00"), email: "late@example.com", lines: []dashLine{{hat, 7, 10000, 0}}},
		// The week before: Rina's first order, so she is returning.
		{status: "completed", placed: wib("2026-08-28 12:00"), email: "rina@example.com", lines: []dashLine{{tee, 4, 10000, 0}}},
		// A purged guest: no email, no phone, left out of customers.
		{status: "paid", placed: wib("2026-09-06 08:00"), lines: []dashLine{{hat, 1, 6000, 0}}},
	} {
		dashOrder(ctx, t, store, tenantID, o)
	}

	code, d := do("GET", "/v1/dashboard?from=2026-09-01&to=2026-09-08", nil)
	if code != 200 {
		t.Fatalf("dashboard: %d %v", code, d)
	}
	equal := func(what string, got any, want map[string]any) {
		t.Helper()
		m, _ := got.(map[string]any)
		for k, v := range want {
			if m[k] != v {
				t.Errorf("%s.%s = %v, want %v (%v)", what, k, m[k], v, m)
			}
		}
	}
	n := func(v int) float64 { return float64(v) }
	// 30000 + 20000 + 10000 + 50000 + 6000.
	equal("current", d["current"], map[string]any{"sales": n(116000), "orders": n(5), "average_order_value": n(23200), "items_sold": n(12)})
	equal("previous", d["previous"], map[string]any{"sales": n(40000), "orders": n(1), "average_order_value": n(40000), "items_sold": n(4)})
	equal("needs_action", d["needs_action"], map[string]any{"awaiting_payment": n(1), "transfers_to_confirm": n(1),
		"ready_to_ship": n(3), "shipped": n(1), "refund_owed": n(1)})
	equal("customers", d["customers"], map[string]any{"new": n(2), "returning": n(1)})
	if d["from"] != "2026-09-01T00:00:00+07:00" || d["to"] != "2026-09-08T00:00:00+07:00" {
		t.Errorf("period %v .. %v", d["from"], d["to"])
	}

	days, _ := d["daily"].([]any)
	if len(days) != 7 {
		t.Fatalf("%d days, want 7: %v", len(days), days)
	}
	for i, want := range []map[string]any{
		{"date": "2026-09-01", "sales": n(30000), "orders": n(1), "items_sold": n(3)},
		{"date": "2026-09-02", "sales": n(0), "orders": n(0), "items_sold": n(0)},
		{"date": "2026-09-03", "sales": n(20000), "orders": n(1), "items_sold": n(2)},
		{"date": "2026-09-04", "sales": n(0), "orders": n(0)},
		{"date": "2026-09-05", "sales": n(10000), "orders": n(1), "items_sold": n(1)},
		{"date": "2026-09-06", "sales": n(6000), "orders": n(1), "items_sold": n(1)},
		{"date": "2026-09-07", "sales": n(50000), "orders": n(1), "items_sold": n(5)},
	} {
		equal(fmt.Sprintf("daily[%d]", i), days[i], want)
	}
	splits, _ := d["by_source"].([]any)
	if len(splits) != 2 {
		t.Fatalf("by_source: %v", splits)
	}
	equal("by_source[0]", splits[0], map[string]any{"source": "manual", "sales": n(66000), "orders": n(4)})
	equal("by_source[1]", splits[1], map[string]any{"source": "storefront", "sales": n(50000), "orders": n(1)})
	splits, _ = d["by_payment_method"].([]any)
	if len(splits) != 2 {
		t.Fatalf("by_payment_method: %v", splits)
	}
	equal("by_payment_method[0]", splits[0], map[string]any{"payment_method": "bank_transfer", "sales": n(66000), "orders": n(4)})
	equal("by_payment_method[1]", splits[1], map[string]any{"payment_method": "midtrans", "sales": n(50000), "orders": n(1)})
	top, _ := d["top_products"].([]any)
	if len(top) != 2 {
		t.Fatalf("top_products: %v", top)
	}
	// Tee 2+1+5 at 10000; Hat 1 at 10000, 2 at 12000 less 4000, and 1 at 6000.
	equal("top_products[0]", top[0], map[string]any{"title": "Tee", "items_sold": n(8), "sales": n(80000)})
	equal("top_products[1]", top[1], map[string]any{"title": "Hat", "items_sold": n(4), "sales": n(36000)})
	names := func(p any) []string {
		var out []string
		for _, c := range p.(map[string]any)["categories"].([]any) {
			c := c.(map[string]any)
			out = append(out, c["name"].(string)+"@"+c["path"].(string))
		}
		return out
	}
	if got := names(top[0]); fmt.Sprint(got) != "[Apparel@apparel Tees@apparel.tees]" {
		t.Errorf("Tee's categories %v, want the main tree by path", got)
	}
	if cats, ok := top[1].(map[string]any)["categories"].([]any); !ok || len(cats) != 0 {
		t.Errorf("Hat's categories %v, want []", top[1].(map[string]any)["categories"])
	}
	// Dewi 50000; Rina 30000 + 10000, by email whatever its case, named as on
	// her latest order; Sari 20000 by phone. The purged guest is no one.
	buyers, _ := d["top_customers"].([]any)
	if len(buyers) != 3 {
		t.Fatalf("top_customers: %v", buyers)
	}
	equal("top_customers[0]", buyers[0], map[string]any{"name": "Dewi", "sales": n(50000), "orders": n(1), "customer_id": nil})
	equal("top_customers[1]", buyers[1], map[string]any{"name": "Rina Wati", "sales": n(40000), "orders": n(2)})
	equal("top_customers[2]", buyers[2], map[string]any{"name": "Sari", "sales": n(20000), "orders": n(1)})

	t.Run("a customer with an account is named and linked by it", func(t *testing.T) {
		account := uuid.Must(uuid.NewV7())
		if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO customers (id, tenant_id, email, name) VALUES ($1, $2, $3, 'Budi Santoso')`,
				account, tenantID, "budi-"+account.String()+"@example.com")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		// Two orders on the account under different snapshot emails still are
		// one customer.
		dashOrder(ctx, t, store, tenantID, dashSeed{status: "completed", placed: wib("2021-03-01 10:00"), name: "Budi",
			email: "old@example.com", customer: &account, lines: []dashLine{{tee, 1, 10000, 0}}})
		dashOrder(ctx, t, store, tenantID, dashSeed{status: "paid", placed: wib("2021-03-02 10:00"), name: "Budi Santoso",
			email: "new@example.com", customer: &account, lines: []dashLine{{tee, 2, 10000, 0}}})
		code, d := do("GET", "/v1/dashboard?from=2021-03-01&to=2021-03-08", nil)
		if code != 200 {
			t.Fatalf("%d %v", code, d)
		}
		buyers, _ := d["top_customers"].([]any)
		if len(buyers) != 1 {
			t.Fatalf("top_customers: %v", buyers)
		}
		equal("top_customers[0]", buyers[0], map[string]any{"name": "Budi Santoso", "sales": n(30000), "orders": n(2),
			"customer_id": account.String()})
	})

	t.Run("an empty period is zeros, every day and every split still listed", func(t *testing.T) {
		code, d := do("GET", "/v1/dashboard?from=2020-01-01T00:00:00%2B07:00&to=2020-01-03T00:00:00%2B07:00", nil)
		if code != 200 {
			t.Fatalf("%d %v", code, d)
		}
		equal("current", d["current"], map[string]any{"sales": n(0), "orders": n(0), "average_order_value": n(0), "items_sold": n(0)})
		if days, _ := d["daily"].([]any); len(days) != 2 {
			t.Errorf("daily %v", d["daily"])
		}
		if s, _ := d["by_source"].([]any); len(s) != 2 {
			t.Errorf("by_source %v", d["by_source"])
		}
		if top, _ := d["top_products"].([]any); top == nil || len(top) != 0 {
			t.Errorf("top_products %v, want []", d["top_products"])
		}
		if buyers, _ := d["top_customers"].([]any); buyers == nil || len(buyers) != 0 {
			t.Errorf("top_customers %v, want []", d["top_customers"])
		}
	})
	t.Run("a bad period is 422 on the field", func(t *testing.T) {
		for path, field := range map[string]string{
			"/v1/dashboard?from=2026-09-08&to=2026-09-01":          "to",
			"/v1/dashboard?from=2026-09-01&to=2026-09-01":          "to",
			"/v1/dashboard?from=2024-01-01&to=2026-01-01":          "to",
			"/v1/dashboard?from=2026-09-01T00:00:00&to=2026-09-08": "from",
			"/v1/dashboard?from=&to=2026-09-08":                    "from",
		} {
			code, p := do("GET", path, nil)
			assertProblem(t, code, p, 422, "validation_failed", field)
		}
	})
}
