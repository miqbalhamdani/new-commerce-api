package storefront

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

func checkoutBody() map[string]any {
	return map[string]any{
		"contact": map[string]any{"name": "Rina", "email": "Rina@Example.com", "phone": "+6281234567890"},
		"shipping_address": map[string]any{"line1": "Jl. Melati 12", "line2": nil, "city": "Bandung",
			"province": "Jawa Barat", "postal_code": "40115"},
		"payment_method": "bank_transfer",
		"note":           "Tolong dibungkus kado",
	}
}

// cartWith is a new cart holding qty of each variant.
func cartWith(t *testing.T, s shop, qty map[string]int) string {
	t.Helper()
	code, cart, raw := s.do(t, "POST", "/v1/storefront/carts", nil)
	if code != 201 {
		t.Fatalf("create cart: %d %s", code, raw)
	}
	id := cart["cart_id"].(string)
	for variant, n := range qty {
		if code, _, raw := s.do(t, "PUT", "/v1/storefront/carts/"+id+"/items/"+variant, map[string]any{"qty": n}); code != 200 {
			t.Fatalf("add %s: %d %s", variant, code, raw)
		}
	}
	return id
}

// TestCheckout is P1-205's acceptance (04-api-spec.md §11.7; BR-046, BR-088,
// BR-089, BR-090, BR-091).
func TestCheckout(t *testing.T) {
	store := openStore(t)
	tenantA, keyA := seedKey(t, store, "https://a.example")
	_, keyB := seedKey(t, store, "https://b.example")
	seedCatalog(t, store, tenantA)
	a, b := newShop(t, store, keyA), newShop(t, store, keyB)
	sale := variantBySKU(t, store, tenantA, "TS-BLK-S")
	full := variantBySKU(t, store, tenantA, "TS-BLK-M")

	t.Run("a guest checkout is priced on the server, once", func(t *testing.T) {
		cart := cartWith(t, a, map[string]int{sale: 2})
		path := "/v1/storefront/carts/" + cart + "/checkout"
		code, o, raw := a.do(t, "POST", path, checkoutBody())
		if code != 201 || o["status"] != "pending" || !strings.HasPrefix(o["order_number"].(string), "SF-") ||
			o["subtotal"] != float64(29800000) || o["shipping"] != float64(0) || o["total"] != float64(29800000) ||
			o["payment_method"] != "bank_transfer" || o["payment"] != nil || o["shipping_courier"] != nil {
			t.Fatalf("checkout: %d %s", code, raw)
		}
		line := o["lines"].([]any)[0].(map[string]any)
		if line["sku"] != "TS-BLK-S" || line["title"] != "Basic Tee — Black / S" || line["qty"] != float64(2) || line["unit_price"] != float64(14900000) {
			t.Errorf("line %v", line)
		}
		token, _ := o["order_token"].(string)
		if len(token) < 40 || !strings.HasSuffix(o["placed_at"].(string), "+07:00") {
			t.Errorf("token %q placed_at %v", token, o["placed_at"])
		}

		// A replay is the same order with 200, token and all (BR-088, BR-091).
		code, again, _ := a.do(t, "POST", path, checkoutBody())
		if code != 200 || again["order_number"] != o["order_number"] || again["order_token"] != token {
			t.Errorf("replay: %d %v", code, again)
		}
		// The cart is spent.
		if code, _, _ := a.get(t, "/v1/storefront/carts/"+cart); code != 404 {
			t.Errorf("checked-out cart GET: %d, want 404", code)
		}

		// A storefront order, with the snapshots and its one audit row,
		// recorded with no actor: a shopper is not staff (BR-018, BR-076).
		ctx := t.Context()
		var source, customer, address string
		var cartID *uuid.UUID
		var audits int
		var actor *uuid.UUID
		_ = store.InTenantTx(tenant.NewContext(ctx, tenantA), func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `SELECT source, cart_id, customer::text, shipping_address::text FROM orders WHERE order_number = $1`,
				o["order_number"]).Scan(&source, &cartID, &customer, &address); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `SELECT count(*), max(actor_id::text)::uuid FROM audit_log a JOIN orders o ON o.id::text = a.subject_id
				WHERE o.order_number = $1 AND a.action = 'order.create'`, o["order_number"]).Scan(&audits, &actor)
		})
		if source != "storefront" || cartID == nil || cartID.String() != cart || audits != 1 || actor != nil {
			t.Errorf("stored: source %s cart %v audits %d actor %v", source, cartID, audits, actor)
		}
		if !strings.Contains(customer, `"rina@example.com"`) || !strings.Contains(address, `"40115"`) {
			t.Errorf("snapshots: %s %s", customer, address)
		}
	})

	t.Run("twenty taps make one order, and every tap gets it", func(t *testing.T) {
		// A pool smaller than the crowd: the nineteen waiting on the cart's
		// lock must not starve the one holding it.
		small, err := db.New(t.Context(), config.AppDatabaseURL()+"&pool_max_conns=4")
		if err != nil {
			t.Fatalf("small pool: %v", err)
		}
		defer small.Close()
		cart := cartWith(t, a, map[string]int{sale: 1, full: 1})
		crowd := newShop(t, small, keyA)

		type answer struct {
			code         int
			number, toke string
		}
		answers := make([]answer, 20)
		var wg sync.WaitGroup
		for i := range answers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				code, o, _ := crowd.do(t, "POST", "/v1/storefront/carts/"+cart+"/checkout", checkoutBody())
				n, _ := o["order_number"].(string)
				tok, _ := o["order_token"].(string)
				answers[i] = answer{code, n, tok}
			}()
		}
		wg.Wait()
		created := 0
		for _, ans := range answers {
			if ans.code == 201 {
				created++
			}
			if ans.code != 201 && ans.code != 200 || ans.number != answers[0].number || ans.toke != answers[0].toke || ans.number == "" {
				t.Errorf("tap: %+v, want the one order %s", ans, answers[0].number)
			}
		}
		var orders int
		ctx := t.Context()
		_ = store.InTenantTx(tenant.NewContext(ctx, tenantA), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM orders WHERE cart_id = $1`, cart).Scan(&orders)
		})
		if created != 1 || orders != 1 {
			t.Errorf("%d created and %d orders, want exactly one", created, orders)
		}
	})

	t.Run("an unavailable item stops checkout and leaves the cart alone", func(t *testing.T) {
		cart := cartWith(t, a, map[string]int{sale: 1, full: 2})
		execTenant(t, store, tenantA, `UPDATE variants SET archived_at = now() WHERE sku = 'TS-BLK-M'`)
		defer execTenant(t, store, tenantA, `UPDATE variants SET archived_at = NULL WHERE sku = 'TS-BLK-M'`)

		code, body, raw := a.do(t, "POST", "/v1/storefront/carts/"+cart+"/checkout", checkoutBody())
		assertProblem(t, "unavailable", code, body, 409, "item_unavailable", "items")
		if errs := body["errors"].([]any); len(errs) != 1 || errs[0].(map[string]any)["sku"] != "TS-BLK-M" ||
			errs[0].(map[string]any)["variant_id"] != full || errs[0].(map[string]any)["title"] != "Basic Tee — Black / M" {
			t.Errorf("errors: %s", raw)
		}
		if _, c, _ := a.get(t, "/v1/storefront/carts/"+cart); len(c["items"].([]any)) != 2 || c["item_count"] != float64(3) {
			t.Errorf("cart after a refused checkout: %v", c)
		}
	})

	t.Run("a sale that ends while the item waits is charged at the regular price", func(t *testing.T) {
		cart := cartWith(t, a, map[string]int{sale: 1})
		execTenant(t, store, tenantA, `UPDATE variants SET sale_ends_at = now() WHERE sku = 'TS-BLK-S'`)
		code, o, raw := a.do(t, "POST", "/v1/storefront/carts/"+cart+"/checkout", checkoutBody())
		if code != 201 || o["total"] != float64(19900000) {
			t.Errorf("after the sale: %d %s", code, raw)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		empty := cartWith(t, a, nil)
		code, body, _ := a.do(t, "POST", "/v1/storefront/carts/"+empty+"/checkout", checkoutBody())
		assertProblem(t, "empty cart", code, body, 422, "empty_cart", "")

		cart := cartWith(t, a, map[string]int{full: 1})
		path := "/v1/storefront/carts/" + cart + "/checkout"
		bad := func(edit func(map[string]any)) map[string]any {
			b := checkoutBody()
			edit(b)
			return b
		}
		for what, req := range map[string]struct {
			body  map[string]any
			code  string
			field string
		}{
			"no name":          {bad(func(b map[string]any) { b["contact"].(map[string]any)["name"] = " " }), "validation_failed", "contact.name"},
			"no address":       {bad(func(b map[string]any) { delete(b, "shipping_address") }), "validation_failed", "shipping_address.line1"},
			"bad postal code":  {bad(func(b map[string]any) { b["shipping_address"].(map[string]any)["postal_code"] = "401" }), "validation_failed", "shipping_address.postal_code"},
			"midtrans, not on": {bad(func(b map[string]any) { b["payment_method"] = "midtrans" }), "validation_failed", "payment_method"},
			"a total (BR-089)": {bad(func(b map[string]any) { b["total"] = 1 }), "unknown_field", "total"},
			"a nested price":   {bad(func(b map[string]any) { b["contact"].(map[string]any)["price"] = 1 }), "unknown_field", "price"},
			"a shipping price": {bad(func(b map[string]any) { b["shipping"] = 0 }), "unknown_field", "shipping"},
		} {
			code, body, _ := a.do(t, "POST", path, req.body)
			assertProblem(t, what, code, body, 422, req.code, req.field)
		}
		code, body, _ = b.do(t, "POST", path, checkoutBody())
		assertProblem(t, "another shop's key", code, body, 404, "not_found", "")
		code, body, _ = a.do(t, "POST", "/v1/storefront/carts/"+uuid.NewString()+"/checkout", checkoutBody())
		assertProblem(t, "unknown cart", code, body, 404, "not_found", "")

		// None of it made an order.
		var n int
		ctx := context.WithoutCancel(t.Context())
		_ = store.InTenantTx(tenant.NewContext(ctx, tenantA), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM orders WHERE cart_id IN ($1, $2)`, empty, cart).Scan(&n)
		})
		if n != 0 {
			t.Errorf("%d orders from refused checkouts", n)
		}
	})
}
