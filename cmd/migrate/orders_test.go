package main

import (
	"context"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// TestOrdersSchema is the schema half of P1-100: each table matches
// 03-erd.md §3.5–3.6 column for column (minus orders.cart_id, which arrives
// with P1-204), read back from PostgreSQL.
func TestOrdersSchema(t *testing.T) {
	ctx := t.Context()
	conn := migratedOwner(ctx, t)

	for _, tt := range []struct {
		table   string
		columns []column
	}{
		{"customers", []column{
			{"id", "uuid", false},
			{"tenant_id", "uuid", false},
			{"email", "text", false},
			{"password_hash", "text", true}, // null for a provider-only customer (BR-127)
			{"name", "text", false},
			{"phone", "text", true},
			{"email_verified_at", "timestamp with time zone", true},
			{"created_at", "timestamp with time zone", false},
			{"updated_at", "timestamp with time zone", false},
		}},
		{"order_sequences", []column{
			{"tenant_id", "uuid", false},
			{"last_value", "bigint", false},
		}},
		{"orders", []column{
			{"id", "uuid", false},
			{"tenant_id", "uuid", false},
			{"source", "text", false},
			{"customer_id", "uuid", true},
			{"order_number", "text", false},
			{"status", "text", false},
			{"customer", "jsonb", false},
			{"shipping_address", "jsonb", false},
			{"note", "text", true},
			{"subtotal_amount", "bigint", false},
			{"shipping_amount", "bigint", false},
			{"discount_amount", "bigint", false},
			{"total_amount", "bigint", false},
			{"currency", "character(3)", false},
			{"payment_method", "text", false},
			{"shipping_courier", "text", true},
			{"shipping_service", "text", true},
			{"courier", "text", true},
			{"tracking_number", "text", true},
			{"placed_at", "timestamp with time zone", false},
			{"paid_at", "timestamp with time zone", true},
			{"shipped_at", "timestamp with time zone", true},
			{"completed_at", "timestamp with time zone", true},
			{"cancelled_at", "timestamp with time zone", true},
			{"refunded_at", "timestamp with time zone", true},
			{"version", "integer", false},
			{"created_at", "timestamp with time zone", false},
			{"updated_at", "timestamp with time zone", false},
		}},
		{"order_lines", []column{
			{"id", "uuid", false},
			{"tenant_id", "uuid", false},
			{"order_id", "uuid", false},
			{"variant_id", "uuid", false},
			{"sku_snapshot", "text", false},
			{"title_snapshot", "text", false},
			{"qty", "integer", false},
			{"unit_price", "bigint", false},
			{"discount_amount", "bigint", false},
		}},
	} {
		t.Run(tt.table+" columns", func(t *testing.T) {
			got := columnsOf(ctx, t, conn, tt.table)
			if !slices.Equal(got, tt.columns) {
				t.Errorf("schema does not match 03-erd.md §3.5–3.6\ngot:  %v\nwant: %v",
					got, tt.columns)
			}
		})
	}

	t.Run("order_number unique per tenant", func(t *testing.T) {
		if !hasUnique(ctx, t, conn, "orders", []string{"tenant_id", "order_number"}) {
			t.Error("UNIQUE (tenant_id, order_number) is missing (BR-077)")
		}
	})
}

// seedOrder inserts a minimal pending order and returns its id. attempt wraps a
// statement in a savepoint so a refused one does not poison the transaction.
func seedOrder(ctx context.Context, t *testing.T, tx pgx.Tx, tenant uuid.UUID, number string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := tx.Exec(ctx, `INSERT INTO orders (id, tenant_id, source, order_number, placed_at)
		VALUES ($1, $2, 'manual', $3, now())`, id, tenant, number); err != nil {
		t.Fatalf("seed order: %v", err)
	}
	return id
}

func attemptSQL(ctx context.Context, t *testing.T, tx pgx.Tx, sql string, args ...any) error {
	t.Helper()
	if _, err := tx.Exec(ctx, `SAVEPOINT a`); err != nil {
		t.Fatalf("savepoint: %v", err)
	}
	_, err := tx.Exec(ctx, sql, args...)
	if err != nil {
		_, _ = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT a`)
	}
	return err
}

// TestOrderChecks is P1-100's acceptance: the shipped-needs-tracking and
// refund CHECKs refuse direct SQL (BR-072, BR-075), the totals arithmetic is
// enforced, and archiving a variant leaves its order lines intact (BR-045).
func TestOrderChecks(t *testing.T) {
	ctx := t.Context()
	tx := beginRolledBack(ctx, t, migratedOwner(ctx, t))
	tenant := seedTenant(ctx, t, tx)

	t.Run("shipped needs courier and tracking even over direct SQL", func(t *testing.T) {
		id := seedOrder(ctx, t, tx, tenant, "SCH-000001")
		if attemptSQL(ctx, t, tx, `UPDATE orders SET status = 'shipped' WHERE id = $1`, id) == nil {
			t.Error("shipped with no courier and tracking was accepted (BR-072)")
		}
		if attemptSQL(ctx, t, tx, `UPDATE orders SET status = 'completed', courier = 'jne' WHERE id = $1`, id) == nil {
			t.Error("completed with no tracking_number was accepted (BR-072)")
		}
		if err := attemptSQL(ctx, t, tx,
			`UPDATE orders SET status = 'shipped', courier = 'jne', tracking_number = 'JNE01' WHERE id = $1`, id); err != nil {
			t.Errorf("shipped with courier and tracking was refused: %v", err)
		}
	})

	t.Run("refund only on a cancelled order that was paid, over direct SQL", func(t *testing.T) {
		id := seedOrder(ctx, t, tx, tenant, "SCH-000002")
		if attemptSQL(ctx, t, tx, `UPDATE orders SET refunded_at = now() WHERE id = $1`, id) == nil {
			t.Error("a refund on a pending order was accepted (BR-075)")
		}
		if attemptSQL(ctx, t, tx,
			`UPDATE orders SET status = 'cancelled', cancelled_at = now(), refunded_at = now() WHERE id = $1`, id) == nil {
			t.Error("a refund on a cancelled, never-paid order was accepted (BR-075)")
		}
		if err := attemptSQL(ctx, t, tx,
			`UPDATE orders SET status = 'cancelled', paid_at = now(), cancelled_at = now(), refunded_at = now() WHERE id = $1`, id); err != nil {
			t.Errorf("a refund on a cancelled, paid order was refused: %v", err)
		}
	})

	t.Run("total must equal subtotal + shipping - discount", func(t *testing.T) {
		id := seedOrder(ctx, t, tx, tenant, "SCH-000003")
		if attemptSQL(ctx, t, tx, `UPDATE orders SET total_amount = 100 WHERE id = $1`, id) == nil {
			t.Error("a total that does not add up was accepted")
		}
		if err := attemptSQL(ctx, t, tx,
			`UPDATE orders SET subtotal_amount = 300, shipping_amount = 50, discount_amount = 100, total_amount = 250 WHERE id = $1`, id); err != nil {
			t.Errorf("a consistent total was refused: %v", err)
		}
	})

	t.Run("archiving a variant leaves its order lines intact", func(t *testing.T) {
		productID, variantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
		if _, err := tx.Exec(ctx, `INSERT INTO products (id, tenant_id, title, slug) VALUES ($1, $2, 'Tee', 'tee-orders')`,
			productID, tenant); err != nil {
			t.Fatalf("product: %v", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO variants (id, tenant_id, product_id, sku) VALUES ($1, $2, $3, 'TEE-1')`,
			variantID, tenant, productID); err != nil {
			t.Fatalf("variant: %v", err)
		}
		orderID := seedOrder(ctx, t, tx, tenant, "SCH-000004")
		if _, err := tx.Exec(ctx, `INSERT INTO order_lines (id, tenant_id, order_id, variant_id, sku_snapshot, title_snapshot, qty, unit_price)
			VALUES ($1, $2, $3, $4, 'TEE-1', 'Tee', 1, 1000)`, uuid.Must(uuid.NewV7()), tenant, orderID, variantID); err != nil {
			t.Fatalf("line: %v", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE variants SET archived_at = now() WHERE id = $1`, variantID); err != nil {
			t.Fatalf("archive variant: %v", err)
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM order_lines WHERE order_id = $1`, orderID).Scan(&n); err != nil {
			t.Fatalf("count lines: %v", err)
		}
		if n != 1 {
			t.Errorf("order has %d lines after archiving its variant, want 1 (BR-045)", n)
		}
	})

	t.Run("an order cannot take another tenant's customer", func(t *testing.T) {
		other := seedTenant(ctx, t, tx)
		customerID := uuid.Must(uuid.NewV7())
		if _, err := tx.Exec(ctx, `INSERT INTO customers (id, tenant_id, email, name) VALUES ($1, $2, 'rina@example.com', 'Rina')`,
			customerID, other); err != nil {
			t.Fatalf("customer: %v", err)
		}
		if attemptSQL(ctx, t, tx, `INSERT INTO orders (id, tenant_id, source, order_number, placed_at, customer_id)
			VALUES ($1, $2, 'storefront', 'SCH-000005', now(), $3)`,
			uuid.Must(uuid.NewV7()), tenant, customerID) == nil {
			t.Error("an order referenced another tenant's customer (BR-004)")
		}
	})

	t.Run("customers email is lower-case and unique per tenant", func(t *testing.T) {
		if attemptSQL(ctx, t, tx, `INSERT INTO customers (id, tenant_id, email, name) VALUES ($1, $2, 'Rina@Example.com', 'Rina')`,
			uuid.Must(uuid.NewV7()), tenant) == nil {
			t.Error("a mixed-case email was accepted (BR-092)")
		}
		if err := attemptSQL(ctx, t, tx, `INSERT INTO customers (id, tenant_id, email, name) VALUES ($1, $2, 'dewi@example.com', 'Dewi')`,
			uuid.Must(uuid.NewV7()), tenant); err != nil {
			t.Fatalf("first customer: %v", err)
		}
		if attemptSQL(ctx, t, tx, `INSERT INTO customers (id, tenant_id, email, name) VALUES ($1, $2, 'dewi@example.com', 'Dewi 2')`,
			uuid.Must(uuid.NewV7()), tenant) == nil {
			t.Error("a duplicate email in one tenant was accepted (BR-092)")
		}
	})
}
