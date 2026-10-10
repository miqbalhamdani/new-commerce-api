package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/orders"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// Sample customers and orders, so the Phase 2 screens have something to
// show. Customers are inserted directly -- nothing in the product creates
// them until Phase 3's storefront. Orders go through the real order service,
// so numbers, snapshots, totals and the audit trail are what production
// would write; only their timestamps are moved into the past afterwards.

// markerEmail is the first sample customer; its presence means the samples
// are already in. A run that fails partway leaves it behind -- delete the
// shop's sample customers and orders to seed again.
const markerEmail = "rina.wati@example.com"

type sampleCustomer struct {
	name, email, phone string
	city               int // index into addresses
}

var sampleCustomers = []sampleCustomer{
	{"Rina Wati", markerEmail, "+6281234567801", 0},
	{"Dewi Lestari", "dewi.lestari@example.com", "+6281234567802", 1},
	{"Budi Santoso", "budi.santoso@example.com", "+6281234567803", 2},
	{"Sari Putri", "sari.putri@example.com", "+6281234567804", 3},
	{"Agus Pratama", "agus.pratama@example.com", "+6281234567805", 4},
	{"Putri Ayu", "putri.ayu@example.com", "+6281234567806", 5},
}

var addresses = []map[string]any{
	{"line1": "Jl. Melati 12", "line2": nil, "city": "Bandung", "province": "Jawa Barat", "postal_code": "40115"},
	{"line1": "Jl. Kenanga 4", "line2": "Blok C", "city": "Surabaya", "province": "Jawa Timur", "postal_code": "60231"},
	{"line1": "Jl. Sudirman 88", "line2": nil, "city": "Jakarta Selatan", "province": "DKI Jakarta", "postal_code": "12190"},
	{"line1": "Jl. Malioboro 21", "line2": nil, "city": "Yogyakarta", "province": "DI Yogyakarta", "postal_code": "55213"},
	{"line1": "Jl. Gatot Subroto 7", "line2": "Lantai 2", "city": "Medan", "province": "Sumatera Utara", "postal_code": "20112"},
	{"line1": "Jl. Sunset Road 45", "line2": nil, "city": "Denpasar", "province": "Bali", "postal_code": "80361"},
}

// sampleOrder is one order's story. customer >= 0 links a sample customer as
// a storefront order; -1 is a manual WhatsApp order for walkIn.
type sampleOrder struct {
	ago      time.Duration // placed this long before now
	customer int
	walkIn   string
	final    string // pending, paid, processing, shipped, completed, cancelled, owed, refunded
}

const day = 24 * time.Hour

// Oldest first, so order numbers rise with placed_at. Every saved view gets
// rows: 3 pending, 2 paid + 2 processing, 2 shipped, 1 refund owed.
var sampleOrders = []sampleOrder{
	{20 * day, 0, "", "completed"},
	{18 * day, -1, "Hendra Wijaya", "completed"},
	{16 * day, 2, "", "refunded"},
	{14 * day, 1, "", "completed"},
	{12 * day, -1, "Lina Marlina", "cancelled"},
	{10 * day, 3, "", "shipped"},
	{8 * day, 4, "", "owed"},
	{6 * day, -1, "Yusuf Hakim", "shipped"},
	{5 * day, 5, "", "processing"},
	{4 * day, -1, "Maya Sari", "processing"},
	{3 * day, 0, "", "paid"},
	{2 * day, 1, "", "paid"},
	{1 * day, -1, "Rudi Hartono", "pending"},
	{10 * time.Hour, 2, "", "pending"},
	{3 * time.Hour, -1, "Wulan Dari", "pending"},
}

// step is one move after placing, at its offset from placed_at. stamp is
// the column it sets; processing has none (BR-071).
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

var couriers = []string{"jne", "sicepat", "jnt"}

type variantRef struct {
	id    uuid.UUID
	price int64
}

func seedSamples(ctx context.Context, store *db.Store, owner db.AuthUser) error {
	ctx = tenant.NewContext(ctx, owner.TenantID)
	ctx = tenant.NewActorContext(ctx, tenant.Actor{UserID: owner.ID})

	var seeded bool
	if err := store.InTenantTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM customers WHERE email = $1)`, markerEmail).Scan(&seeded)
	}); err != nil {
		return err
	}
	if seeded {
		slog.Info("sample customers and orders already seeded")
		return nil
	}

	variants, err := sampleCatalog(ctx, store)
	if err != nil {
		return fmt.Errorf("sample catalog: %w", err)
	}
	customerIDs, err := insertCustomers(ctx, store)
	if err != nil {
		return fmt.Errorf("sample customers: %w", err)
	}

	svc := orders.NewService(store, nil, nil)
	now := time.Now()
	for i, so := range sampleOrders {
		if err := placeSample(ctx, store, svc, i, so, variants, customerIDs, now); err != nil {
			return fmt.Errorf("sample order %d: %w", i+1, err)
		}
	}
	// Counts in the message: the log allow-list redacts unknown fields (BR-013).
	slog.Info(fmt.Sprintf("seeded %d sample customers and %d orders", len(customerIDs), len(sampleOrders)))
	return nil
}

// sampleCatalog returns live, priced variants to order from, creating two
// draft sample products first when the shop has fewer than three.
func sampleCatalog(ctx context.Context, store *db.Store) ([]variantRef, error) {
	read := func() ([]variantRef, error) {
		var out []variantRef
		err := store.InTenantTx(ctx, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT v.id, variant_price(v)::bigint FROM variants v
				JOIN products p ON p.id = v.product_id
				WHERE v.archived_at IS NULL AND p.archived_at IS NULL AND variant_price(v) > 0
				ORDER BY v.created_at, v.id LIMIT 8`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var v variantRef
				if err := rows.Scan(&v.id, &v.price); err != nil {
					return err
				}
				out = append(out, v)
			}
			return rows.Err()
		})
		return out, err
	}
	have, err := read()
	if err != nil || len(have) >= 3 {
		return have, err
	}

	// Drafts on purpose: they have no category, so the publish check
	// (BR-038) would refuse them as active.
	suffix := uuid.Must(uuid.NewV7()).String()[:8]
	products := []struct {
		title, slug string
		options     []string
		price       int64
		variants    [][]string
	}{
		{"Sample Basic Tee", "sample-basic-tee-" + suffix, []string{"Color", "Size"}, 19900000,
			[][]string{{"Black", "M"}, {"Black", "L"}, {"White", "M"}}},
		{"Sample Cargo Pants", "sample-cargo-pants-" + suffix, []string{"Size"}, 34900000,
			[][]string{{"30"}, {"32"}}},
	}
	err = store.InTenantTx(ctx, func(tx pgx.Tx) error {
		for _, p := range products {
			productID := uuid.Must(uuid.NewV7())
			if _, err := tx.Exec(ctx, `INSERT INTO products (id, tenant_id, title, slug, option_names, status)
				VALUES ($1, current_setting('app.tenant_id')::uuid, $2, $3, $4, 'draft')`,
				productID, p.title, p.slug, p.options); err != nil {
				return err
			}
			for _, opts := range p.variants {
				sku := strings.ToUpper("SMP-" + strings.Join(opts, "-") + "-" + suffix)
				if _, err := tx.Exec(ctx, `INSERT INTO variants
					(id, tenant_id, product_id, sku, option_values, regular_price_amount, weight_grams)
					VALUES ($1, current_setting('app.tenant_id')::uuid, $2, $3, $4, $5, 300)`,
					uuid.Must(uuid.NewV7()), productID, sku, opts, p.price); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slog.Info("created two draft sample products to order from")
	return read()
}

// insertCustomers writes the sample customers. They share one hash of a
// sample password, so the accounts are valid when Phase 3 can sign in.
func insertCustomers(ctx context.Context, store *db.Store) ([]uuid.UUID, error) {
	hash, err := auth.HashPassword("sample-password")
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(sampleCustomers))
	err = store.InTenantTx(ctx, func(tx pgx.Tx) error {
		for i, c := range sampleCustomers {
			ids[i] = uuid.Must(uuid.NewV7())
			since := time.Now().Add(-time.Duration(60-i*8) * day)
			if _, err := tx.Exec(ctx, `INSERT INTO customers
				(id, tenant_id, email, password_hash, name, phone, email_verified_at, created_at, updated_at)
				VALUES ($1, current_setting('app.tenant_id')::uuid, $2, $3, $4, $5, $6, $6, $6)`,
				ids[i], c.email, hash, c.name, c.phone, since); err != nil {
				return err
			}
		}
		return nil
	})
	return ids, err
}

func placeSample(ctx context.Context, store *db.Store, svc *orders.Service, i int, so sampleOrder,
	variants []variantRef, customerIDs []uuid.UUID, now time.Time) error {
	var snapshot map[string]any
	addr := addresses[i%len(addresses)]
	if so.customer >= 0 {
		c := sampleCustomers[so.customer]
		snapshot = map[string]any{"name": c.name, "email": c.email, "phone": c.phone}
		addr = addresses[c.city]
	} else {
		snapshot = map[string]any{"name": so.walkIn, "email": nil, "phone": fmt.Sprintf("+62857%08d", 31000+i*17)}
	}

	in := orders.CreateInput{Shipping: 1500000 + int64(i%3)*500000}
	for j := 0; j < 1+i%3; j++ {
		v := variants[(i*2+j)%len(variants)]
		line := orders.CreateLine{VariantID: v.id, Qty: 1 + (i+j)%3}
		if so.customer < 0 && j == 0 && i%2 == 0 {
			line.Discount = v.price * int64(line.Qty) / 10 // 10% off, haggled on WhatsApp
		}
		in.Lines = append(in.Lines, line)
	}
	if so.customer < 0 {
		note := "Order via WhatsApp"
		in.Note = &note
	}
	var err error
	if in.Customer, err = json.Marshal(snapshot); err != nil {
		return err
	}
	if in.ShippingAddress, err = json.Marshal(addr); err != nil {
		return err
	}

	id, err := svc.Create(ctx, in)
	if err != nil {
		return err
	}
	courier := couriers[i%len(couriers)]
	if so.customer >= 0 {
		// Create only makes manual orders; a storefront order is the same row
		// linked to its customer, with the courier the shopper chose.
		if err := store.InTenantTx(ctx, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE orders SET source = 'storefront', customer_id = $2,
				shipping_courier = $3, shipping_service = 'reg' WHERE id = $1`,
				id, customerIDs[so.customer], courier)
			return err
		}); err != nil {
			return err
		}
	}
	placed := now.Add(-so.ago)
	if err := backdate(ctx, store, id, "placed_at", placed); err != nil {
		return err
	}

	path, ok := paths[so.final]
	if !ok {
		return errors.New("unknown final state " + so.final)
	}
	for _, s := range path {
		switch {
		case s.refund:
			note := "BCA transfer back to the customer"
			_, err = svc.Refund(ctx, id, &note)
		case s.to == "shipped":
			tracking := fmt.Sprintf("%s%010d", strings.ToUpper(courier), 4400000000+int64(i)*7919)
			_, err = svc.Transition(ctx, id, s.to, orders.TransitionInput{Courier: &courier, TrackingNumber: &tracking})
		case s.to == "cancelled" && so.final != "cancelled":
			reason := "Customer changed their mind"
			_, err = svc.Transition(ctx, id, s.to, orders.TransitionInput{Reason: &reason})
		default:
			_, err = svc.Transition(ctx, id, s.to, orders.TransitionInput{})
		}
		if err != nil {
			return fmt.Errorf("%s: %w", s.to, err)
		}
		if err := backdate(ctx, store, id, s.stamp, placed.Add(s.after)); err != nil {
			return err
		}
	}
	return nil
}

// backdate moves one step into the past: the step's timestamp column (from
// the fixed set in paths, never user input) and the order's newest audit
// row, so the history reads like days of work rather than one second.
func backdate(ctx context.Context, store *db.Store, id uuid.UUID, stamp string, at time.Time) error {
	return store.InTenantTx(ctx, func(tx pgx.Tx) error {
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
