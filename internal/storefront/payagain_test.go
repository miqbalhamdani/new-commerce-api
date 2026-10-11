package storefront

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// TestPayAgain is P1-222's acceptance (04-api-spec.md §11.10; BR-123,
// BR-126).
func TestPayAgain(t *testing.T) {
	ctx := t.Context()
	store := openStore(t)
	tenantID, key := seedKey(t, store, "https://a.example")
	seedCatalog(t, store, tenantID)
	shipsFrom(t, store, tenantID)
	takesMidtrans(t, store, tenantID)
	a := newShop(t, store, key)
	full := variantBySKU(t, store, tenantID, "TS-BLK-M")

	attempts := func(number string) []string {
		t.Helper()
		var ids []string
		c := context.WithoutCancel(ctx)
		_ = store.InTenantTx(tenant.NewContext(c, tenantID), func(tx pgx.Tx) error {
			rows, err := tx.Query(c, `SELECT p.provider_order_id || ':' || p.status FROM payments p
				JOIN orders o ON o.id = p.order_id WHERE o.order_number = $1 ORDER BY p.attempt`, number)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var s string
				_ = rows.Scan(&s)
				ids = append(ids, s)
			}
			return rows.Err()
		})
		return ids
	}
	place := func(body map[string]any) (string, string) {
		t.Helper()
		_, o, raw := a.do(t, "POST", "/v1/storefront/carts/"+cartWith(t, a, map[string]int{full: 1})+"/checkout", body)
		number, _ := o["order_number"].(string)
		if number == "" {
			t.Fatalf("checkout: %s", raw)
		}
		token, _ := o["order_token"].(string)
		return number, token
	}
	payAgain := func(number string, headers ...string) (int, map[string]any) {
		code, body, _ := a.do(t, "POST", "/v1/storefront/orders/"+number+"/payment", nil, headers...)
		return code, body
	}

	t.Run("a live link is reused", func(t *testing.T) {
		number, token := place(midtransBody())
		snaps := a.pay.snapCount()
		code, link := payAgain(number, "X-Order-Token", token)
		if code != 200 || link["snap_token"] != "tok-"+number || a.pay.snapCount() != snaps {
			t.Errorf("reuse: %d %v (%d snaps, was %d)", code, link, a.pay.snapCount(), snaps)
		}
	})

	t.Run("no link, or an expired one, gets a new attempt", func(t *testing.T) {
		a.pay.setDown(true)
		number, token := place(midtransBody())
		a.pay.setDown(false)
		code, link := payAgain(number, "X-Order-Token", token)
		if code != 200 || link["snap_token"] != "tok-"+number+"-2" {
			t.Fatalf("after a failed checkout link: %d %v", code, link)
		}
		execTenant(t, store, tenantID, `UPDATE payments SET expires_at = now() - interval '1 second'
			WHERE provider_order_id = '`+number+`-2'`)
		if code, link = payAgain(number, "X-Order-Token", token); link["snap_token"] != "tok-"+number+"-3" {
			t.Errorf("after expiry: %d %v", code, link)
		}
		want := []string{number + ":failed", number + "-2:failed", number + "-3:pending"}
		if got := attempts(number); len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
			t.Errorf("attempts %v, want %v", got, want)
		}
	})

	t.Run("Midtrans down is 502, and the attempt is kept", func(t *testing.T) {
		a.pay.setDown(true)
		number, token := place(midtransBody())
		code, body := payAgain(number, "X-Order-Token", token)
		assertProblem(t, "down", code, body, 502, "payment_unavailable", "")
		a.pay.setDown(false)
		if code, link := payAgain(number, "X-Order-Token", token); code != 200 || link["snap_token"] != "tok-"+number+"-3" {
			t.Errorf("after: %d %v; attempt -2 must not be offered again", code, link)
		}
	})

	t.Run("two taps at once get one link", func(t *testing.T) {
		a.pay.setDown(true)
		number, token := place(midtransBody())
		a.pay.setDown(false)
		links := make([]any, 2)
		var wg sync.WaitGroup
		for i := range links {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, link := payAgain(number, "X-Order-Token", token)
				links[i] = link["snap_token"]
			}()
		}
		wg.Wait()
		if links[0] != links[1] || len(attempts(number)) != 2 {
			t.Errorf("links %v, attempts %v", links, attempts(number))
		}
	})

	t.Run("whose it is", func(t *testing.T) {
		number, token := place(midtransBody())
		for what, headers := range map[string][]string{
			"no token":      nil,
			"a wrong token": {"X-Order-Token", token[:len(token)-2] + "xx"},
		} {
			code, body := payAgain(number, headers...)
			assertProblem(t, what, code, body, 401, "customer_auth_required", "")
		}
		code, body := payAgain("SF-999999", "X-Order-Token", token)
		assertProblem(t, "an unknown number", code, body, 401, "customer_auth_required", "")

		rina := signUp(t, a, "rina@example.com")
		code, body = payAgain(number, "Authorization", "Bearer "+rina["access_token"].(string))
		assertProblem(t, "a customer, not theirs", code, body, 404, "not_found", "")
		mine := midtransBody()
		delete(mine, "contact")
		_, o, _ := a.do(t, "POST", "/v1/storefront/carts/"+cartWith(t, a, map[string]int{full: 1})+"/checkout", mine,
			"Authorization", "Bearer "+rina["access_token"].(string))
		if code, _ := payAgain(o["order_number"].(string), "Authorization", "Bearer "+rina["access_token"].(string)); code != 200 {
			t.Errorf("a customer, theirs: %d", code)
		}
	})

	t.Run("nothing to pay", func(t *testing.T) {
		paid, token := place(midtransBody())
		execTenant(t, store, tenantID, `UPDATE orders SET status = 'paid', paid_at = now() WHERE order_number = '`+paid+`'`)
		code, body := payAgain(paid, "X-Order-Token", token)
		assertProblem(t, "paid", code, body, 422, "validation_failed", "order_number")
		bank, token := place(checkoutBody())
		code, body = payAgain(bank, "X-Order-Token", token)
		assertProblem(t, "bank transfer", code, body, 422, "validation_failed", "order_number")
	})
}
