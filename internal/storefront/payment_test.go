package storefront

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

func midtransBody() map[string]any {
	b := checkoutBody()
	b["payment_method"] = "midtrans"
	return b
}

// TestMidtransCheckout is P1-220's acceptance (04-api-spec.md §11.7; BR-122,
// BR-123): an attempt for the order's total, named after the order, a Snap
// link that a replay returns, and an order that survives Midtrans being down.
func TestMidtransCheckout(t *testing.T) {
	store := openStore(t)
	tenantID, key := seedKey(t, store, "https://a.example")
	seedCatalog(t, store, tenantID)
	shipsFrom(t, store, tenantID)
	a := newShop(t, store, key)
	full := variantBySKU(t, store, tenantID, "TS-BLK-M")

	cart := cartWith(t, a, map[string]int{full: 1})
	code, body, _ := a.do(t, "POST", "/v1/storefront/carts/"+cart+"/checkout", midtransBody())
	assertProblem(t, "Midtrans off", code, body, 422, "validation_failed", "payment_method")

	hook := takesMidtrans(t, store, tenantID)
	code, o, raw := a.do(t, "POST", "/v1/storefront/carts/"+cart+"/checkout", midtransBody())
	if code != 201 || o["payment_method"] != "midtrans" || o["payment_error"] != nil {
		t.Fatalf("checkout: %d %s", code, raw)
	}
	number := o["order_number"].(string)
	pay, _ := o["payment"].(map[string]any)
	if pay["snap_token"] != "tok-"+number || pay["redirect_url"] != "https://pay.test/"+number {
		t.Errorf("payment %v", pay)
	}
	if exp, _ := time.Parse(time.RFC3339, pay["expires_at"].(string)); time.Until(exp) < 23*time.Hour {
		t.Errorf("link expires %v, want 24 hours out", exp)
	}

	// What Midtrans was told: the order's total in whole rupiah, the shop's
	// own key, and where to report back (BR-006, BR-124).
	snap := a.pay.snaps[0]["transaction_details"].(map[string]any)
	if snap["order_id"] != number || snap["gross_amount"] != o["total"].(float64)/100 {
		t.Errorf("snap %v for total %v", snap, o["total"])
	}
	if a.pay.auths[0] != "Basic "+base64.StdEncoding.EncodeToString([]byte("Mid-server-TEST:")) ||
		a.pay.hooks[0] != "http://api.test/v1/webhooks/midtrans/"+hook.String() {
		t.Errorf("auth %q, notification %q", a.pay.auths[0], a.pay.hooks[0])
	}
	var attempt int
	var provider string
	var gross int64
	ctx := t.Context()
	_ = store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT attempt, provider_order_id, gross_amount FROM payments`).Scan(&attempt, &provider, &gross)
	})
	if attempt != 1 || provider != number || gross != int64(o["total"].(float64)) {
		t.Errorf("attempt %d %s %d", attempt, provider, gross)
	}

	// A replay returns the live link, without asking Midtrans again.
	code, again, _ := a.do(t, "POST", "/v1/storefront/carts/"+cart+"/checkout", midtransBody())
	if code != 200 || again["payment"].(map[string]any)["snap_token"] != pay["snap_token"] || a.pay.snapCount() != 1 {
		t.Errorf("replay: %d %v (%d snaps)", code, again, a.pay.snapCount())
	}
	_, view, _ := a.do(t, "GET", "/v1/storefront/orders/"+number, nil, "X-Order-Token", o["order_token"].(string))
	if view["payment_status"] != "pending" {
		t.Errorf("payment_status %v, want pending", view["payment_status"])
	}

	t.Run("Midtrans down keeps the order", func(t *testing.T) {
		a.pay.setDown(true)
		defer a.pay.setDown(false)
		down := cartWith(t, a, map[string]int{full: 1})
		code, o, raw := a.do(t, "POST", "/v1/storefront/carts/"+down+"/checkout", midtransBody())
		if code != 201 || o["payment"] != nil || o["payment_error"] != "payment_unavailable" || o["order_number"] == nil {
			t.Fatalf("down: %d %s", code, raw)
		}
		code, again, _ := a.do(t, "POST", "/v1/storefront/carts/"+down+"/checkout", midtransBody())
		if code != 200 || again["payment"] != nil || again["payment_error"] != "payment_unavailable" {
			t.Errorf("replay with no link: %d %v", code, again)
		}
	})

	t.Run("a total Midtrans cannot take", func(t *testing.T) {
		execTenant(t, store, tenantID, `UPDATE variants SET regular_price_amount = 19900050 WHERE sku = 'TS-BLK-M'`)
		defer execTenant(t, store, tenantID, `UPDATE variants SET regular_price_amount = 21900000 WHERE sku = 'TS-BLK-M'`)
		sen := cartWith(t, a, map[string]int{full: 1})
		code, body, _ := a.do(t, "POST", "/v1/storefront/carts/"+sen+"/checkout", midtransBody())
		assertProblem(t, "sen in the total", code, body, 422, "validation_failed", "payment_method")
		var n int
		_ = store.InTenantTx(tenant.NewContext(context.WithoutCancel(ctx), tenantID), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM orders WHERE cart_id = $1`, sen).Scan(&n)
		})
		if n != 0 {
			t.Error("an order was made that Midtrans could never take")
		}
	})
}
