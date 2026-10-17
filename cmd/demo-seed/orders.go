package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/orders"
)

// Customers are inserted directly, as dev-seed does: staff cannot create one
// (BR-092) and signing up is the storefront's. Orders go through the order
// service, so numbers, snapshots, totals and history are what production
// writes; then their timestamps move into the past, step by step.

func (sd *seeder) insertCustomers(ctx context.Context, now time.Time) ([]uuid.UUID, error) {
	hash, err := auth.HashPassword(rand.Text()) // nobody signs in as a demo customer
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(customers))
	err = sd.store.InTenantTx(ctx, func(tx pgx.Tx) error {
		for i, c := range customers {
			ids[i] = uuid.Must(uuid.NewV7())
			since := now.Add(-time.Duration(c.since) * day)
			var verified *time.Time
			if c.verified {
				v := since.Add(10 * time.Minute)
				verified = &v
			}
			h := &hash
			if c.google { // signed up with Google: no password
				h = nil
			}
			if _, err := tx.Exec(ctx, `INSERT INTO customers
				(id, tenant_id, email, password_hash, name, phone, email_verified_at, created_at, updated_at)
				VALUES ($1, current_setting('app.tenant_id')::uuid, $2, $3, $4, $5, $6, $7, $7)`,
				ids[i], c.email, h, c.name, c.phone, verified, since); err != nil {
				return fmt.Errorf("%s: %w", c.email, err)
			}
			if c.google {
				if _, err := tx.Exec(ctx, `INSERT INTO customer_identities (tenant_id, customer_id, provider, subject, email, created_at)
					VALUES (current_setting('app.tenant_id')::uuid, $1, 'google', $2, $3, $4)`,
					ids[i], fmt.Sprintf("1%020d", 104729+i*7919), c.email, since); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return ids, err
}

type variantRef struct {
	id       uuid.UUID
	price    int64
	retiring bool // its product is archived after the orders
}

// step is one move after placing, at its offset from placed_at; stamp is the
// column it sets (processing has none, BR-071). The same paths as dev-seed.
type step struct {
	to     string
	after  time.Duration
	stamp  string
	refund bool
}

var (
	pay      = step{to: "paid", after: 2 * time.Hour, stamp: "paid_at"}
	process  = step{to: "processing", after: 5 * time.Hour}
	ship     = step{to: "shipped", after: 26 * time.Hour, stamp: "shipped_at"}
	complete = step{to: "completed", after: 4 * day, stamp: "completed_at"}
	cancel   = step{to: "cancelled", after: 7 * time.Hour, stamp: "cancelled_at"}
	refund   = step{after: 2 * day, stamp: "refunded_at", refund: true}
)

var paths = map[string][]step{
	"pending":    nil,
	"paid":       {pay},
	"processing": {pay, process},
	"shipped":    {pay, process, ship},
	"completed":  {pay, process, ship, complete},
	"cancelled":  {{to: "cancelled", after: 3 * time.Hour, stamp: "cancelled_at"}},
	"owed":       {pay, cancel},
	"refunded":   {pay, cancel, refund},
}

// The storefront's couriers, the service a shopper picks, and the fee.
var couriers = []struct {
	code, service string
	fee           int64
}{{"jne", "reg", 1800000}, {"jnt", "ez", 1600000}, {"sicepat", "reg", 1500000}, {"anteraja", "reg", 1400000}}

func (sd *seeder) seedOrders(t *teamCtx, cat catalogResult, customerIDs []uuid.UUID, now time.Time) (int, error) {
	pool, err := sd.orderable(t.ctx, cat)
	if err != nil {
		return 0, err
	}
	next := 0
	for i, o := range orderPlan {
		if err := sd.place(t, i, o, pool, customerIDs, &next, now); err != nil {
			return i, fmt.Errorf("order %d: %w", i+1, err)
		}
	}
	return len(orderPlan), nil
}

// orderable is every live, priced variant of a product on sale -- drafts are
// never ordered, products to be archived are, until a month ago.
func (sd *seeder) orderable(ctx context.Context, cat catalogResult) ([]variantRef, error) {
	state := map[uuid.UUID]string{}
	for _, p := range cat.products {
		state[p.id] = p.def.state
	}
	var out []variantRef
	err := sd.store.InTenantTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT v.id, v.product_id, variant_price(v)::bigint FROM variants v
			WHERE v.archived_at IS NULL AND variant_price(v) > 0 ORDER BY v.created_at, v.id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v variantRef
			var product uuid.UUID
			if err := rows.Scan(&v.id, &product, &v.price); err != nil {
				return err
			}
			if s := state[product]; s != "draft" {
				v.retiring = s == "archived"
				out = append(out, v)
			}
		}
		return rows.Err()
	})
	if err == nil && len(out) == 0 {
		err = errors.New("no variant to order")
	}
	return out, err
}

// place makes one order and walks it to its final state. Staff type in the
// manual ones; shoppers place the rest. Bank transfers are confirmed by the
// admin, Midtrans payments by its notification (no actor); ops pack and ship
// -- the since-disabled Fajar did so until six weeks ago -- and the owner
// refunds.
func (sd *seeder) place(t *teamCtx, i int, o demoOrder, pool []variantRef, customerIDs []uuid.UUID, next *int, now time.Time) error {
	placed := now.Add(-o.ago)
	courier := couriers[i%len(couriers)]
	in := orders.CreateInput{Shipping: courier.fee + int64(i%3)*400000}
	snapshot := map[string]any{"email": nil} // nobody to email, whatever a demo clicks
	addr := addresses[i%len(addresses)]
	var customer *uuid.UUID
	reseller := false
	if o.via == manual {
		w := walkIns[i%len(walkIns)]
		snapshot["name"], snapshot["phone"] = w.name, w.phone
		in.Note = &w.note
		reseller = strings.HasPrefix(w.note, "Reseller")
	} else {
		c := pickCustomer(placed, now, next)
		snapshot["name"], snapshot["phone"] = customers[c].name, customers[c].phone
		addr, customer = addresses[customers[c].city], &customerIDs[c]
		if i%5 == 0 {
			in.Note = &storefrontNotes[i%len(storefrontNotes)]
		}
	}

	used := map[uuid.UUID]bool{}
	for j := 0; len(in.Lines) < 1+i%3 && j < 20; j++ {
		v := pool[(i*37+j*53)%len(pool)]
		if used[v.id] || (v.retiring && o.ago < 30*day) {
			continue
		}
		used[v.id] = true
		line := orders.CreateLine{VariantID: v.id, Qty: 1 + (i+j)%2}
		if reseller {
			line.Qty += 2
			line.Discount = v.price * int64(line.Qty) / 10 / 100 * 100 // 10%, to the rupiah
		}
		in.Lines = append(in.Lines, line)
	}
	var err error
	if in.Customer, err = json.Marshal(snapshot); err != nil {
		return err
	}
	if in.ShippingAddress, err = json.Marshal(addr); err != nil {
		return err
	}

	ctx := t.ctx
	if o.via == manual {
		ctx = t.as(t.admin)
	}
	id, err := sd.orders.Create(ctx, in)
	if err != nil {
		return err
	}
	if o.via != manual {
		// Create only makes manual orders; a storefront order is the same row
		// linked to its customer, with the courier and payment the shopper chose.
		method := "bank_transfer"
		if o.via == midtrans {
			method = "midtrans"
		}
		if err := sd.store.InTenantTx(t.ctx, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.ctx, `UPDATE orders SET source = 'storefront', customer_id = $2, shipping_courier = $3,
				shipping_service = $4, payment_method = $5 WHERE id = $1`, id, customer, courier.code, courier.service, method)
			return err
		}); err != nil {
			return err
		}
	}
	if err := sd.backdate(t.ctx, id, "placed_at", placed); err != nil {
		return err
	}
	paidAt := placed.Add(pay.after)
	if o.via == midtrans {
		if paidAt, err = sd.payments(t.ctx, id, i, o.final, placed); err != nil {
			return fmt.Errorf("payments: %w", err)
		}
	}

	for _, s := range paths[o.final] {
		when := placed.Add(s.after)
		worker := t.ops
		if o.ago > 42*day {
			worker = t.former
		}
		switch {
		case s.refund:
			note := "Dana dikembalikan lewat transfer BCA"
			if o.via == midtrans {
				note = "Dana dikembalikan lewat dashboard Midtrans"
			}
			_, err = sd.orders.Refund(t.as(t.owner), id, &note)
		case s.to == "paid":
			ctx := t.as(t.admin)
			if o.via == midtrans {
				ctx, when = t.ctx, paidAt
			}
			_, err = sd.orders.Transition(ctx, id, s.to, orders.TransitionInput{})
		case s.to == "shipped":
			tracking := fmt.Sprintf("%s%010d", strings.ToUpper(courier.code[:3]), 2600000000+int64(i)*7919)
			_, err = sd.orders.Transition(t.as(worker), id, s.to, orders.TransitionInput{Courier: &courier.code, TrackingNumber: &tracking})
		case s.to == "cancelled":
			reason := cancelReasons[o.via]
			if o.final != "cancelled" {
				reason = "Pembeli minta dibatalkan setelah membayar"
			}
			_, err = sd.orders.Transition(t.as(t.admin), id, s.to, orders.TransitionInput{Reason: &reason})
		default:
			_, err = sd.orders.Transition(t.as(worker), id, s.to, orders.TransitionInput{})
		}
		if err != nil {
			return fmt.Errorf("%s: %w", orDefault(s.to, "refund"), err)
		}
		if err := sd.backdate(t.ctx, id, s.stamp, when); err != nil {
			return err
		}
	}
	return nil
}

// pickCustomer is the next customer, in turn, who had signed up by then.
func pickCustomer(placed, now time.Time, next *int) int {
	for k := range customers {
		c := (*next + k) % len(customers)
		if now.Add(-time.Duration(customers[c].since) * day).Before(placed) {
			*next = c + 1
			return c
		}
	}
	return 0
}

// payments writes a Midtrans order's attempts as checkout and the webhook
// would have left them: a pending one awaiting payment, an expired
// one on a cancelled order, or a paid one -- after a declined card, every
// other time. It returns when the paid attempt settled.
func (sd *seeder) payments(ctx context.Context, id uuid.UUID, i int, final string, placed time.Time) (time.Time, error) {
	type attempt struct {
		status, tx string
		at         time.Time
	}
	var attempts []attempt
	switch final {
	case "pending":
		attempts = []attempt{{"pending", "pending", placed.Add(time.Minute)}}
	case "cancelled":
		attempts = []attempt{{"failed", "expire", placed.Add(time.Minute)}}
	default:
		if i%2 == 0 {
			attempts = []attempt{{"failed", "deny", placed.Add(time.Minute)}, {"paid", "settlement", placed.Add(35 * time.Minute)}}
		} else {
			attempts = []attempt{{"paid", "settlement", placed.Add(4 * time.Minute)}}
		}
	}
	var paidAt time.Time
	err := sd.store.InTenantTx(ctx, func(tx pgx.Tx) error {
		var number string
		var total int64
		if err := tx.QueryRow(ctx, `SELECT order_number, total_amount FROM orders WHERE id = $1`, id).Scan(&number, &total); err != nil {
			return err
		}
		for n, a := range attempts {
			providerID := number
			if n > 0 {
				providerID = fmt.Sprintf("%s-%d", number, n+1)
			}
			var paid *time.Time
			if a.status == "paid" {
				p := a.at.Add(3 * time.Minute)
				paid, paidAt = &p, p
			}
			if _, err := tx.Exec(ctx, `INSERT INTO payments (id, tenant_id, order_id, provider_order_id, attempt, status,
				gross_amount, expires_at, transaction_status, paid_at, created_at, updated_at)
				VALUES ($1, current_setting('app.tenant_id')::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)`,
				uuid.Must(uuid.NewV7()), id, providerID, n+1, a.status, total, a.at.Add(24*time.Hour), a.tx, paid, a.at); err != nil {
				return err
			}
		}
		return nil
	})
	return paidAt, err
}

// backdate moves one step into the past: the step's timestamp column (from
// the fixed set in paths, never user input) and the order's newest audit row,
// so the history reads like days of work rather than one second. dev-seed's.
func (sd *seeder) backdate(ctx context.Context, id uuid.UUID, stamp string, at time.Time) error {
	return sd.store.InTenantTx(ctx, func(tx pgx.Tx) error {
		set := "updated_at = $2"
		switch stamp {
		case "placed_at":
			set += ", placed_at = $2, created_at = $2"
		case "paid_at", "shipped_at", "completed_at", "cancelled_at", "refunded_at":
			set += ", " + stamp + " = $2"
		case "":
		default:
			return errors.New("backdate: unexpected column " + stamp)
		}
		if _, err := tx.Exec(ctx, `UPDATE orders SET `+set+` WHERE id = $1`, id, at); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE audit_log SET created_at = $2
			WHERE id = (SELECT max(id) FROM audit_log WHERE subject_type = 'order' AND subject_id = $1)`,
			id.String(), at)
		return err
	})
}
