package storefront

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// TestMeAndOrderHistory is P1-208's acceptance (04-api-spec.md §11.9;
// BR-082, BR-091, BR-095).
func TestMeAndOrderHistory(t *testing.T) {
	store := openStore(t)
	tenantA, keyA := seedKey(t, store, "https://a.example")
	tenantB, keyB := seedKey(t, store, "https://b.example")
	seedCatalog(t, store, tenantA)
	shipsFrom(t, store, tenantA)
	t.Cleanup(func() { cleanCatalog(t, store, tenantB) })
	a, b := newShop(t, store, keyA), newShop(t, store, keyB)
	full := variantBySKU(t, store, tenantA, "TS-BLK-M")

	rina := signUp(t, a, "rina@example.com")
	asRina := "Bearer " + rina["access_token"].(string)
	dewi := signUp(t, a, "dewi@example.com")
	asDewi := "Bearer " + dewi["access_token"].(string)

	t.Run("me", func(t *testing.T) {
		code, body, _ := a.get(t, "/v1/storefront/me")
		assertProblem(t, "a key alone (BR-082)", code, body, 401, "customer_auth_required", "")
		code, me, raw := a.do(t, "GET", "/v1/storefront/me", nil, "Authorization", asRina)
		if code != 200 || me["email"] != "rina@example.com" || me["phone"] != "+6281234567890" {
			t.Fatalf("me: %d %s", code, raw)
		}
		code, me, raw = a.do(t, "PATCH", "/v1/storefront/me", map[string]any{"name": "Rina Wijaya", "phone": nil}, "Authorization", asRina)
		if code != 200 || me["name"] != "Rina Wijaya" || me["phone"] != nil || me["email"] != "rina@example.com" {
			t.Errorf("patch: %d %s", code, raw)
		}
		for what, req := range map[string]struct {
			body      map[string]any
			code, fld string
		}{
			"null name":     {map[string]any{"name": nil}, "validation_failed", "name"},
			"blank name":    {map[string]any{"name": " "}, "validation_failed", "name"},
			"an email (v2)": {map[string]any{"email": "x@example.com"}, "validation_failed", "email"},
			"unknown field": {map[string]any{"vip": true}, "unknown_field", "vip"},
		} {
			code, body, _ := a.do(t, "PATCH", "/v1/storefront/me", req.body, "Authorization", asRina)
			assertProblem(t, what, code, body, 422, req.code, req.fld)
		}
	})

	t.Run("a guest cart follows the shopper, and the order goes into their history", func(t *testing.T) {
		cart := cartWith(t, a, map[string]int{full: 1}) // as a guest
		if code, _, raw := a.do(t, "PUT", "/v1/storefront/carts/"+cart+"/items/"+full, map[string]any{"qty": 2}, "Authorization", asRina); code != 200 {
			t.Fatalf("signed-in edit: %d %s", code, raw)
		}
		// Dewi holding the id does not take Rina's cart.
		a.do(t, "GET", "/v1/storefront/carts/"+cart, nil, "Authorization", asDewi)
		var owner *uuid.UUID
		ctx := t.Context()
		_ = store.InTenantTx(tenant.NewContext(ctx, tenantA), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT customer_id FROM carts WHERE id = $1`, cart).Scan(&owner)
		})
		if owner == nil || owner.String() != rina["customer"].(map[string]any)["id"] {
			t.Errorf("cart owner %v, want Rina", owner)
		}

		body := checkoutBody()
		delete(body, "contact") // the profile is the contact
		code, o, raw := a.do(t, "POST", "/v1/storefront/carts/"+cart+"/checkout", body, "Authorization", asRina)
		if code != 201 || o["order_token"] != nil {
			t.Fatalf("signed-in checkout: %d %s", code, raw)
		}
		number := o["order_number"].(string)

		code, list, raw := a.do(t, "GET", "/v1/storefront/me/orders", nil, "Authorization", asRina)
		if code != 200 || len(list["data"].([]any)) != 1 {
			t.Fatalf("my orders: %d %s", code, raw)
		}
		row := list["data"].([]any)[0].(map[string]any)
		if row["order_number"] != number || row["item_count"] != float64(2) || row["status"] != "pending" {
			t.Errorf("row %v", row)
		}
		code, detail, raw := a.do(t, "GET", "/v1/storefront/me/orders/"+number, nil, "Authorization", asRina)
		addr, _ := detail["shipping_address"].(map[string]any)
		if code != 200 || detail["payment_status"] != nil || detail["discount"] != float64(0) || addr["postal_code"] != "40115" ||
			len(detail["lines"].([]any)) != 1 {
			t.Errorf("detail: %d %s", code, raw)
		}

		code, body2, _ := a.do(t, "GET", "/v1/storefront/me/orders/"+number, nil, "Authorization", asDewi)
		assertProblem(t, "another customer's order (BR-011)", code, body2, 404, "not_found", "")
		if _, l, _ := a.do(t, "GET", "/v1/storefront/me/orders", nil, "Authorization", asDewi); len(l["data"].([]any)) != 0 {
			t.Errorf("Dewi sees %v", l)
		}
		code, body2, _ = a.get(t, "/v1/storefront/me/orders")
		assertProblem(t, "history with a key alone (BR-082)", code, body2, 401, "customer_auth_required", "")
		// No order token was ever issued for it, so the guest route cannot open it.
		code, body2, _ = a.do(t, "GET", "/v1/storefront/orders/"+number, nil, "X-Order-Token", "anything")
		assertProblem(t, "a customer's order on the guest route", code, body2, 401, "customer_auth_required", "")
	})

	t.Run("history pages newest first", func(t *testing.T) {
		for range 2 {
			cart := cartWith(t, a, map[string]int{full: 1})
			body := checkoutBody()
			delete(body, "contact")
			if code, _, raw := a.do(t, "POST", "/v1/storefront/carts/"+cart+"/checkout", body, "Authorization", asRina); code != 201 {
				t.Fatalf("checkout: %d %s", code, raw)
			}
		}
		var seen []string
		path := "/v1/storefront/me/orders?limit=2"
		for range 3 {
			_, page, _ := a.do(t, "GET", path, nil, "Authorization", asRina)
			for _, row := range page["data"].([]any) {
				seen = append(seen, row.(map[string]any)["order_number"].(string))
			}
			next, _ := page["next_cursor"].(string)
			if next == "" {
				break
			}
			path = "/v1/storefront/me/orders?limit=2&cursor=" + next
		}
		if len(seen) != 3 || seen[0] < seen[1] || seen[1] < seen[2] {
			t.Errorf("history %v, want three, newest first", seen)
		}
	})

	t.Run("a guest's order opens with its own token only (BR-091)", func(t *testing.T) {
		place := func() (string, string) {
			_, o, _ := a.do(t, "POST", "/v1/storefront/carts/"+cartWith(t, a, map[string]int{full: 1})+"/checkout", checkoutBody())
			return o["order_number"].(string), o["order_token"].(string)
		}
		n1, t1 := place()
		n2, _ := place()
		code, o, raw := a.do(t, "GET", "/v1/storefront/orders/"+n1, nil, "X-Order-Token", t1)
		if code != 200 || o["order_number"] != n1 || o["status"] != "pending" {
			t.Fatalf("guest lookup: %d %s", code, raw)
		}
		for what, req := range map[string][2]string{
			"no token":                     {n1, ""},
			"a wrong token":                {n1, t1[:len(t1)-2] + "xx"},
			"one order's token on another": {n2, t1},
			"an order that does not exist": {"SF-999999", t1},
		} {
			code, body, _ := a.do(t, "GET", "/v1/storefront/orders/"+req[0], nil, "X-Order-Token", req[1])
			assertProblem(t, what, code, body, 401, "customer_auth_required", "")
		}
		code, body, _ := b.do(t, "GET", "/v1/storefront/orders/"+n1, nil, "X-Order-Token", t1)
		assertProblem(t, "another shop's key", code, body, 401, "customer_auth_required", "")
	})
}
