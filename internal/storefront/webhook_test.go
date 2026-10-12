package storefront

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// notification is Midtrans' body for orderID, signed with key.
func notification(orderID, statusCode, gross, transactionStatus, key string) []byte {
	sum := sha512.Sum512([]byte(orderID + statusCode + gross + key))
	b, _ := json.Marshal(map[string]any{"order_id": orderID, "status_code": statusCode, "gross_amount": gross,
		"transaction_status": transactionStatus, "signature_key": hex.EncodeToString(sum[:]),
		"payment_type": "bank_transfer", "va_numbers": []any{map[string]any{"bank": "bca"}}})
	return b
}

// TestMidtransWebhook is P1-221's acceptance (04-api-spec.md §11.10; BR-003,
// BR-124, BR-125).
func TestMidtransWebhook(t *testing.T) {
	ctx := t.Context()
	store := openStore(t)
	tenantID, key := seedKey(t, store, "https://a.example")
	seedCatalog(t, store, tenantID)
	shipsFrom(t, store, tenantID)
	hook := takesMidtrans(t, store, tenantID)
	a := newShop(t, store, key)
	full := variantBySKU(t, store, tenantID, "TS-BLK-M")

	type state struct {
		order, attempt string
		paid           bool
		version        int
	}
	read := func(number string) state {
		t.Helper()
		var s state
		c := context.WithoutCancel(ctx)
		_ = store.InTenantTx(tenant.NewContext(c, tenantID), func(tx pgx.Tx) error {
			return tx.QueryRow(c, `SELECT o.status, p.status, o.paid_at IS NOT NULL, o.version FROM orders o
				JOIN payments p ON p.order_id = o.id WHERE o.order_number = $1 ORDER BY p.attempt DESC LIMIT 1`, number).
				Scan(&s.order, &s.attempt, &s.paid, &s.version)
		})
		return s
	}
	place := func() (string, string) {
		t.Helper()
		_, o, raw := a.do(t, "POST", "/v1/storefront/carts/"+cartWith(t, a, map[string]int{full: 1})+"/checkout", midtransBody())
		number, _ := o["order_number"].(string)
		if number == "" {
			t.Fatalf("checkout: %s", raw)
		}
		return number, strconv.FormatInt(int64(o["total"].(float64))/100, 10) + ".00"
	}
	notify := func(body []byte) error { return a.orders.Notify(ctx, hook, body) }

	number, gross := place()

	t.Run("a bad signature changes nothing", func(t *testing.T) {
		a.pay.confirm(number, "settlement", gross)
		err := notify(notification(number, "200", gross, "settlement", "Mid-server-WRONG"))
		if p := apperrors.From(err); p.Status != 401 || read(number).order != "pending" {
			t.Errorf("bad signature: %v, order %+v", err, read(number))
		}
		if err := a.orders.Notify(ctx, uuid.New(), notification(number, "200", gross, "settlement", "Mid-server-TEST")); apperrors.From(err).Status != 404 {
			t.Errorf("unknown webhook: %v", err)
		}
	})

	t.Run("Get Status is believed, not the body", func(t *testing.T) {
		a.pay.confirm(number, "pending", gross)
		if err := notify(notification(number, "200", gross, "settlement", "Mid-server-TEST")); err != nil {
			t.Fatal(err)
		}
		if s := read(number); s.order != "pending" || s.attempt != "pending" {
			t.Errorf("after a body that lies: %+v", s)
		}
	})

	t.Run("settlement pays the order once", func(t *testing.T) {
		a.pay.confirm(number, "settlement", gross)
		if err := notify(notification(number, "200", gross, "settlement", "Mid-server-TEST")); err != nil {
			t.Fatal(err)
		}
		s := read(number)
		if s.order != "paid" || s.attempt != "paid" || !s.paid {
			t.Fatalf("after settlement: %+v", s)
		}
		// A replay is a no-op: no second transition, no version bump.
		if err := notify(notification(number, "200", gross, "settlement", "Mid-server-TEST")); err != nil || read(number).version != s.version {
			t.Errorf("replay: %v, version %d -> %d", err, s.version, read(number).version)
		}
		var audits int
		var actor *uuid.UUID
		_ = store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*), max(actor_id::text)::uuid FROM audit_log a JOIN orders o ON o.id::text = a.subject_id
				WHERE o.order_number = $1 AND a.action = 'order.transition'`, number).Scan(&audits, &actor)
		})
		if audits != 1 || actor != nil {
			t.Errorf("%d transition audit rows (actor %v), want one with no actor", audits, actor)
		}
	})

	t.Run("an amount that does not match never pays", func(t *testing.T) {
		n, _ := place()
		a.pay.confirm(n, "settlement", "1.00")
		if err := notify(notification(n, "200", "1.00", "settlement", "Mid-server-TEST")); err != nil {
			t.Fatal(err)
		}
		if s := read(n); s.order != "pending" || s.attempt != "amount_mismatch" {
			t.Errorf("mismatch: %+v", s)
		}
	})

	t.Run("expire fails the attempt and leaves the order pending", func(t *testing.T) {
		n, g := place()
		a.pay.confirm(n, "expire", g)
		if err := notify(notification(n, "407", g, "expire", "Mid-server-TEST")); err != nil {
			t.Fatal(err)
		}
		if s := read(n); s.order != "pending" || s.attempt != "failed" {
			t.Errorf("expire: %+v", s)
		}
	})

	t.Run("money for an order ops already moved on is recorded, not a loop", func(t *testing.T) {
		n, g := place()
		execTenant(t, store, tenantID, `UPDATE orders SET status = 'cancelled', cancelled_at = now() WHERE order_number = '`+n+`'`)
		a.pay.confirm(n, "settlement", g)
		if err := notify(notification(n, "200", g, "settlement", "Mid-server-TEST")); err != nil {
			t.Fatalf("a settlement on a cancelled order must still answer 200: %v", err)
		}
		if s := read(n); s.order != "cancelled" || s.attempt != "paid" {
			t.Errorf("cancelled: %+v", s)
		}
		var owed int
		_ = store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'payment.after_cancel'`).Scan(&owed)
		})
		if owed != 1 {
			t.Errorf("%d refund-owed audit rows, want 1", owed)
		}
	})

	t.Run("answers the rest without changing anything", func(t *testing.T) {
		if err := notify(notification("NOT-OURS-1", "200", "1.00", "settlement", "Mid-server-TEST")); err != nil {
			t.Errorf("unknown order id: %v", err)
		}
		a.pay.setDown(true)
		defer a.pay.setDown(false)
		err := notify(notification(number, "200", gross, "settlement", "Mid-server-TEST"))
		if p := apperrors.From(err); p.Status != 503 || !strings.Contains(p.Code, "payment_unavailable") {
			t.Errorf("Get Status down: %v, want 503", err)
		}
	})
}
