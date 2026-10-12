package storefront

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/email"
	"github.com/miqbalhamdani/new-commerce-api/internal/orders"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// rupiahString is an order total as Midtrans writes gross_amount: whole
// rupiah, two decimals.
func rupiahString(total float64) string { return strconv.FormatInt(int64(total)/100, 10) + ".00" }

// kinds is the order emails queued so far, by kind.
func kinds(m *mailbox) []string {
	var out []string
	for _, e := range m.all() {
		if strings.HasPrefix(e.Kind, "order_") || e.Kind == "payment_received" {
			out = append(out, e.Kind)
		}
	}
	return out
}

// TestOrderEmails is P1-228's acceptance (BR-128): placed, payment received
// and shipped, each once per event, from the shop's name, with the courier
// and tracking on "shipped".
func TestOrderEmails(t *testing.T) {
	ctx := t.Context()
	store := openStore(t)
	tenantID, key := seedKey(t, store, "https://a.example")
	seedCatalog(t, store, tenantID)
	shipsFrom(t, store, tenantID)
	hook := takesMidtrans(t, store, tenantID)
	execTenant(t, store, tenantID, `UPDATE storefront_settings SET contact_email = 'halo@tokoabc.com',
		bank_transfer_instructions = 'Transfer to BCA 123456789'`)
	a := newShop(t, store, key)
	full := variantBySKU(t, store, tenantID, "TS-BLK-M")
	render := orders.OrderMail(store)
	tctx := tenant.NewContext(ctx, tenantID)
	orderID := func(number string) uuid.UUID {
		var id uuid.UUID
		_ = store.InTenantTx(tctx, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT id FROM orders WHERE order_number = $1`, number).Scan(&id)
		})
		return id
	}

	// A bank transfer checkout: one "placed", even with a replay.
	cart := cartWith(t, a, map[string]int{full: 1})
	_, o, _ := a.do(t, "POST", "/v1/storefront/carts/"+cart+"/checkout", checkoutBody())
	a.do(t, "POST", "/v1/storefront/carts/"+cart+"/checkout", checkoutBody())
	if got := kinds(a.mail); len(got) != 1 || got[0] != "order_placed" {
		t.Fatalf("after checkout and its replay: %v", got)
	}
	bank := orderID(o["order_number"].(string))
	m, err := render(ctx, email.Envelope{Kind: "order_placed", TenantID: tenantID, OrderID: bank})
	if err != nil || m == nil || m.To != "rina@example.com" || m.FromName != "Storefront" || m.ReplyTo != "halo@tokoabc.com" ||
		!strings.Contains(m.Text, "Transfer to BCA 123456789") || !strings.Contains(m.Text, "Rp 230.000") ||
		!strings.Contains(m.Text, "Basic Tee — Black / M") || m.Key != bank.String()+":order_placed" {
		t.Fatalf("placed: %+v %v", m, err)
	}

	// Marked paid by ops, then shipped: one email each, the courier and the
	// tracking number on "shipped". A repeat of either sends nothing.
	for _, step := range []struct{ to, tracking string }{{"paid", ""}, {"paid", ""}, {"processing", ""}, {"shipped", "JNE0123456789"}, {"shipped", "JNE0123456789"}} {
		in := orders.TransitionInput{}
		if step.tracking != "" {
			in.TrackingNumber = &step.tracking
		}
		if _, err := a.orders.Transition(tctx, bank, step.to, in); err != nil {
			t.Fatalf("to %s: %v", step.to, err)
		}
	}
	if got := kinds(a.mail); strings.Join(got, ",") != "order_placed,payment_received,order_shipped" {
		t.Errorf("after paid, processing and shipped, with repeats: %v", got)
	}
	m, _ = render(ctx, email.Envelope{Kind: "order_shipped", TenantID: tenantID, OrderID: bank})
	if m == nil || !strings.Contains(m.Text, "JNE") || !strings.Contains(m.Text, "JNE0123456789") {
		t.Errorf("shipped: %+v", m)
	}

	// Midtrans: marked paid by an operator first, then Midtrans settles --
	// still one "payment received".
	before := len(kinds(a.mail))
	_, mo, _ := a.do(t, "POST", "/v1/storefront/carts/"+cartWith(t, a, map[string]int{full: 1})+"/checkout", midtransBody())
	number := mo["order_number"].(string)
	if _, err := a.orders.Transition(tctx, orderID(number), "paid", orders.TransitionInput{}); err != nil {
		t.Fatal(err)
	}
	a.pay.confirm(number, "settlement", rupiahString(mo["total"].(float64)))
	if err := a.orders.Notify(context.WithoutCancel(ctx), hook, notification(number, "200", rupiahString(mo["total"].(float64)), "settlement", "Mid-server-TEST")); err != nil {
		t.Fatal(err)
	}
	if got := kinds(a.mail)[before:]; strings.Join(got, ",") != "order_placed,payment_received" {
		t.Errorf("mark-paid then settlement: %v, want one payment_received", got)
	}

	// A settlement that pays the order is the email.
	before = len(kinds(a.mail))
	_, mo, _ = a.do(t, "POST", "/v1/storefront/carts/"+cartWith(t, a, map[string]int{full: 1})+"/checkout", midtransBody())
	number = mo["order_number"].(string)
	a.pay.confirm(number, "settlement", rupiahString(mo["total"].(float64)))
	_ = a.orders.Notify(context.WithoutCancel(ctx), hook, notification(number, "200", rupiahString(mo["total"].(float64)), "settlement", "Mid-server-TEST"))
	_ = a.orders.Notify(context.WithoutCancel(ctx), hook, notification(number, "200", rupiahString(mo["total"].(float64)), "settlement", "Mid-server-TEST"))
	if got := kinds(a.mail)[before:]; strings.Join(got, ",") != "order_placed,payment_received" {
		t.Errorf("settlement and its replay: %v", got)
	}

	// No email on the order: nothing to send.
	execTenant(t, store, tenantID, `UPDATE orders SET customer = '{"name":"Walk-in","email":null,"phone":null}' WHERE id = '`+bank.String()+`'`)
	if m, err := render(ctx, email.Envelope{Kind: "order_shipped", TenantID: tenantID, OrderID: bank}); m != nil || err != nil {
		t.Errorf("no email: %+v %v", m, err)
	}
}
