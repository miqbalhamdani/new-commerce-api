package orders_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/orders"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

var statuses = []string{"pending", "paid", "processing", "shipped", "completed", "cancelled"}

// legal is BR-070's table, written out independently of the implementation so
// the test cannot inherit a mistake from the map it checks.
var legal = map[string][]string{
	"pending":    {"paid", "cancelled"},
	"paid":       {"processing", "cancelled"},
	"processing": {"shipped", "cancelled"},
	"shipped":    {"completed"},
}

type harness struct {
	store  *db.Store
	svc    *orders.Service
	ctx    context.Context
	seq    int
	tenant uuid.UUID
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := t.Context()
	store, err := db.New(ctx, config.AppDatabaseURL())
	if err != nil {
		t.Fatalf("connect as app_user: %v", err)
	}
	t.Cleanup(store.Close)

	tenantID := uuid.Must(uuid.NewV7())
	ctx = tenant.NewContext(ctx, tenantID)
	ctx = tenant.NewActorContext(ctx, tenant.Actor{UserID: uuid.Must(uuid.NewV7())})
	h := &harness{store: store, svc: orders.NewService(store, nil, nil), ctx: ctx, tenant: tenantID}

	if err := store.InTenantTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO tenants (id, name, slug, order_prefix)
			VALUES ($1, 'Transition test', $2, 'TRN')`, tenantID, "transition-"+tenantID.String())
		return err
	}); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		cctx := context.WithoutCancel(ctx)
		_ = store.InTenantTx(cctx, func(tx pgx.Tx) error {
			if _, err := tx.Exec(cctx, `DELETE FROM orders`); err != nil {
				return err
			}
			if _, err := tx.Exec(cctx, `DELETE FROM audit_log`); err != nil {
				return err
			}
			_, err := tx.Exec(cctx, `DELETE FROM tenants WHERE id = $1`, tenantID)
			return err
		})
	})
	return h
}

// seedOrder inserts an order already sitting at status, with whatever the
// table CHECKs require of that status.
func (h *harness) seedOrder(t *testing.T, status string, courier *string) uuid.UUID {
	t.Helper()
	h.seq++
	id := uuid.Must(uuid.NewV7())
	var shipped *string
	var paid bool
	if status == "shipped" || status == "completed" {
		s := "JNE0123"
		shipped = &s
		paid = true
	}
	if status == "paid" || status == "processing" {
		paid = true
	}
	if err := h.store.InTenantTx(h.ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(h.ctx, `INSERT INTO orders
			(id, tenant_id, source, order_number, status, placed_at, shipping_courier,
			 courier, tracking_number, paid_at, cancelled_at)
			VALUES ($1, $2, 'manual', $3, $4, now(), $5,
			        CASE WHEN $6::text IS NOT NULL THEN 'jne' END, $6,
			        CASE WHEN $7 THEN now() END,
			        CASE WHEN $4 = 'cancelled' THEN now() END)`,
			id, h.tenant, fmt.Sprintf("TRN-%06d", h.seq), status, courier, shipped, paid)
		return err
	}); err != nil {
		t.Fatalf("seed %s order: %v", status, err)
	}
	return id
}

func (h *harness) read(t *testing.T, id uuid.UUID) sqlcgen.Order {
	t.Helper()
	var o sqlcgen.Order
	if err := h.store.InTenantTx(h.ctx, func(tx pgx.Tx) error {
		var err error
		o, err = sqlcgen.New(tx).GetOrder(h.ctx, id)
		return err
	}); err != nil {
		t.Fatalf("read order: %v", err)
	}
	return o
}

func (h *harness) auditRows(t *testing.T, id uuid.UUID) int {
	t.Helper()
	var n int
	if err := h.store.InTenantTx(h.ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(h.ctx, `SELECT count(*) FROM audit_log WHERE subject_id = $1`, id.String()).Scan(&n)
	}); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	return n
}

// TestTransitionMatrix is P1-101's acceptance: every from × to pair behaves
// per BR-070/BR-071 -- legal moves stamp, bump and audit exactly once; a move
// to the current status is a successful no-op; everything else is a 409 that
// changes nothing.
func TestTransitionMatrix(t *testing.T) {
	h := newHarness(t)

	stampOf := map[string]func(sqlcgen.Order) bool{
		"paid":       func(o sqlcgen.Order) bool { return o.PaidAt != nil },
		"shipped":    func(o sqlcgen.Order) bool { return o.ShippedAt != nil },
		"completed":  func(o sqlcgen.Order) bool { return o.CompletedAt != nil },
		"cancelled":  func(o sqlcgen.Order) bool { return o.CancelledAt != nil },
		"processing": func(o sqlcgen.Order) bool { return true }, // no stamp of its own
		"pending":    func(o sqlcgen.Order) bool { return true },
	}
	ship := func(to string) orders.TransitionInput {
		if to != "shipped" {
			return orders.TransitionInput{}
		}
		tn := "JNE0456"
		return orders.TransitionInput{TrackingNumber: &tn}
	}

	for _, from := range statuses {
		for _, to := range statuses {
			t.Run(from+" to "+to, func(t *testing.T) {
				courier := "jne"
				id := h.seedOrder(t, from, &courier)
				out, err := h.svc.Transition(h.ctx, id, to, ship(to))

				switch {
				case from == to:
					if err != nil {
						t.Fatalf("a move to the current status must succeed: %v", err)
					}
					if out.Version != 1 {
						t.Errorf("a no-op bumped version to %d", out.Version)
					}
					if n := h.auditRows(t, id); n != 0 {
						t.Errorf("a no-op wrote %d audit rows, want 0", n)
					}
				case slices.Contains(legal[from], to):
					if err != nil {
						t.Fatalf("legal move refused: %v", err)
					}
					if out.Status != to {
						t.Errorf("status = %s, want %s", out.Status, to)
					}
					if out.Version != 2 {
						t.Errorf("version = %d, want 2", out.Version)
					}
					if !stampOf[to](out) {
						t.Errorf("%s did not stamp its timestamp", to)
					}
					if n := h.auditRows(t, id); n != 1 {
						t.Errorf("%d audit rows, want exactly 1 (BR-073)", n)
					}
				default:
					var known *apperrors.Error
					if !errors.As(err, &known) || known.Code != apperrors.CodeIllegalTransition {
						t.Fatalf("illegal move: got %v, want illegal_transition", err)
					}
					if after := h.read(t, id); after.Status != from || after.Version != 1 {
						t.Errorf("an illegal move changed the row: status %s version %d", after.Status, after.Version)
					}
					if n := h.auditRows(t, id); n != 0 {
						t.Errorf("an illegal move wrote %d audit rows", n)
					}
				}
			})
		}
	}
}

func TestShipValidation(t *testing.T) {
	h := newHarness(t)

	t.Run("ship without a tracking number is refused", func(t *testing.T) {
		courier := "jne"
		id := h.seedOrder(t, "processing", &courier)
		_, err := h.svc.Transition(h.ctx, id, "shipped", orders.TransitionInput{})
		assertValidation(t, err, "tracking_number")
	})

	t.Run("courier defaults to the shopper's choice", func(t *testing.T) {
		courier := "sicepat"
		id := h.seedOrder(t, "processing", &courier)
		tn := "SC001"
		out, err := h.svc.Transition(h.ctx, id, "shipped", orders.TransitionInput{TrackingNumber: &tn})
		if err != nil {
			t.Fatalf("ship: %v", err)
		}
		if out.Courier == nil || *out.Courier != "sicepat" {
			t.Errorf("courier = %v, want sicepat", out.Courier)
		}
	})

	t.Run("a manual order with no courier anywhere is refused", func(t *testing.T) {
		id := h.seedOrder(t, "processing", nil)
		tn := "X1"
		_, err := h.svc.Transition(h.ctx, id, "shipped", orders.TransitionInput{TrackingNumber: &tn})
		assertValidation(t, err, "courier")
	})

	t.Run("a courier off the code format is refused, not a 500", func(t *testing.T) {
		id := h.seedOrder(t, "processing", nil)
		tn, courier := "X1", "JNE Express"
		_, err := h.svc.Transition(h.ctx, id, "shipped", orders.TransitionInput{TrackingNumber: &tn, Courier: &courier})
		assertValidation(t, err, "courier")
	})
}

func TestRefund(t *testing.T) {
	h := newHarness(t)
	courier := "jne"

	t.Run("only on a cancelled order that was paid, once", func(t *testing.T) {
		pending := h.seedOrder(t, "pending", &courier)
		if _, err := h.svc.Refund(h.ctx, pending, nil); err == nil {
			t.Error("a refund on a pending order was recorded")
		}

		// Cancelled but never paid.
		unpaid := h.seedOrder(t, "pending", &courier)
		if _, err := h.svc.Transition(h.ctx, unpaid, "cancelled", orders.TransitionInput{}); err != nil {
			t.Fatalf("cancel: %v", err)
		}
		if _, err := h.svc.Refund(h.ctx, unpaid, nil); err == nil {
			t.Error("a refund on a cancelled, never-paid order was recorded")
		}

		paid := h.seedOrder(t, "paid", &courier)
		if _, err := h.svc.Transition(h.ctx, paid, "cancelled", orders.TransitionInput{}); err != nil {
			t.Fatalf("cancel: %v", err)
		}
		note := "BCA transfer 6 Oct"
		out, err := h.svc.Refund(h.ctx, paid, &note)
		if err != nil {
			t.Fatalf("refund: %v", err)
		}
		if out.Status != "cancelled" || out.RefundedAt == nil {
			t.Errorf("refund left status %s refunded_at %v", out.Status, out.RefundedAt)
		}
		if out.Version != 3 {
			t.Errorf("version = %d, want 3 (cancel then refund)", out.Version)
		}
		if _, err := h.svc.Refund(h.ctx, paid, nil); err == nil {
			t.Error("a second refund was recorded")
		}
		if n := h.auditRows(t, paid); n != 2 {
			t.Errorf("%d audit rows, want 2 (one transition, one refund)", n)
		}
	})
}

func assertValidation(t *testing.T, err error, field string) {
	t.Helper()
	var known *apperrors.Error
	if !errors.As(err, &known) || known.Code != apperrors.CodeValidationFailed {
		t.Fatalf("got %v, want validation_failed on %s", err, field)
	}
	for _, f := range known.Fields {
		if f.Name == field {
			return
		}
	}
	t.Errorf("validation error does not name %s: %+v", field, known.Fields)
}
