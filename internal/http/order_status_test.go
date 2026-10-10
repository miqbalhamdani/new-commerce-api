package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// Order status routes (P1-102) in the isolation and audit suites. Each route
// is aimed at an order sitting where the move is legal, so a 404 is isolation
// and nothing else.
func init() {
	transitions := []struct {
		path string
		from string
		body map[string]any
	}{
		{"mark-paid", "pending", nil},
		{"process", "paid", nil},
		{"ship", "processing", map[string]any{"tracking_number": "JNE0123"}},
		{"complete", "shipped", nil},
		{"cancel", "pending", nil},
		{"refund", "refundable", nil},
	}
	for _, tr := range transitions {
		isolationCases = append(isolationCases,
			isolationCase{route: route{"POST", "/v1/orders/{id}/" + tr.path}, seed: seedOrderAt(tr.from),
				request: func(t *testing.T, s seeded) *http.Request {
					if tr.body != nil {
						return bodyRequest(t, http.MethodPost, "/v1/orders/"+s.otherID+"/"+tr.path, s.accessToken, tr.body)
					}
					return bearerRequest(t, http.MethodPost, "/v1/orders/"+s.otherID+"/"+tr.path, s.accessToken)
				}})
		auditCases = append(auditCases,
			auditCase{route: route{"POST", "/v1/orders/{id}/" + tr.path},
				request: func(t *testing.T, s seeded) (*http.Request, string, string) {
					ctx := t.Context()
					store := openAppStore(ctx, t)
					tenantID := uuid.MustParse(s.tenantID)
					id, _ := insertOrder(ctx, t, store, tenantID, tr.from, "Audit order")
					if tr.body != nil {
						return bodyRequest(t, http.MethodPost, "/v1/orders/"+id+"/"+tr.path, s.accessToken, tr.body), "order", id
					}
					return bearerRequest(t, http.MethodPost, "/v1/orders/"+id+"/"+tr.path, s.accessToken), "order", id
				}})
	}
}

var orderSeq atomic.Int64

// insertOrder seeds one order already sitting at status ("refundable" is
// cancelled + paid), satisfying the table CHECKs, and returns its id and the
// marker written into the customer snapshot.
func insertOrder(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID, status, name string) (string, string) {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	marker := name + " " + id.String()
	paid := status == "paid" || status == "processing" || status == "shipped" ||
		status == "completed" || status == "refundable"
	cancelled := status == "refundable"
	if cancelled {
		status = "cancelled"
	}
	var courier, tracking *string
	if status == "shipped" || status == "completed" {
		c, tn := "jne", "JNE000"
		courier, tracking = &c, &tn
	}
	if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO orders
			(id, tenant_id, source, order_number, status, placed_at, customer, shipping_courier,
			 courier, tracking_number, paid_at, cancelled_at)
			VALUES ($1, $2, 'manual', $3, $4, now(),
			        jsonb_build_object('name', $5::text, 'email', null, 'phone', null), 'jne',
			        $6, $7, CASE WHEN $8 THEN now() END, CASE WHEN $4 = 'cancelled' THEN now() END)`,
			id, tenantID, fmt.Sprintf("ORD-%06d", orderSeq.Add(1)), status, marker, courier, tracking, paid)
		return err
	}); err != nil {
		t.Fatalf("seed %s order: %v", status, err)
	}
	return id.String(), marker
}

// seedOrderAt is a signed-in admin plus one order at the given status whose
// customer name is the marker.
func seedOrderAt(status string) func(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID) seeded {
	return func(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID) seeded {
		t.Helper()
		s := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
		s.id, s.marker = insertOrder(ctx, t, store, tenantID, status, "Customer")
		return s
	}
}

// TestOrderStatusRoutes is P1-102's acceptance over HTTP (04-api-spec.md §5.3;
// BR-070..075): the semantics themselves are unit-tested in internal/orders.
func TestOrderStatusRoutes(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	tenantID := uuid.Must(uuid.NewV7())
	admin := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	do := apiClient(t, admin)
	post := func(id, action string, body any) (int, map[string]any) {
		t.Helper()
		return do("POST", "/v1/orders/"+id+"/"+action, body)
	}

	t.Run("the walk to completed, with a no-op repeat", func(t *testing.T) {
		id, _ := insertOrder(ctx, t, store, tenantID, "pending", "Rina")

		code, o := post(id, "mark-paid", nil)
		if code != 200 || o["status"] != "paid" || o["version"] != float64(2) || o["paid_at"] == nil {
			t.Fatalf("mark-paid: %d %v", code, o)
		}
		if ps, ok := o["payments"].([]any); !ok || len(ps) != 0 {
			t.Errorf("payments = %v, want []", o["payments"])
		}
		hist := o["history"].([]any)
		if len(hist) != 1 {
			t.Fatalf("history has %d entries, want 1", len(hist))
		}
		h := hist[0].(map[string]any)
		if h["action"] != "order.transition" || h["from"] != "pending" || h["to"] != "paid" ||
			h["actor"] == nil || h["actor"].(map[string]any)["name"] == "" {
			t.Errorf("history entry: %v", h)
		}

		code, o = post(id, "mark-paid", nil)
		if code != 200 || o["version"] != float64(2) || len(o["history"].([]any)) != 1 {
			t.Errorf("a repeat is not a clean no-op: %d %v", code, o)
		}

		if code, o = post(id, "process", nil); code != 200 || o["status"] != "processing" {
			t.Fatalf("process: %d %v", code, o)
		}
		code, o = post(id, "ship", map[string]any{"tracking_number": "JNE0456"})
		if code != 200 || o["status"] != "shipped" || o["courier"] != "jne" || o["tracking_number"] != "JNE0456" {
			t.Fatalf("ship should default courier to the shopper's choice: %d %v", code, o)
		}
		if code, o = post(id, "complete", nil); code != 200 || o["status"] != "completed" {
			t.Fatalf("complete: %d %v", code, o)
		}
		if at := o["allowed_transitions"].([]any); len(at) != 0 {
			t.Errorf("completed still offers transitions: %v", at)
		}
	})

	t.Run("an illegal move is a 409 naming from and to", func(t *testing.T) {
		id, _ := insertOrder(ctx, t, store, tenantID, "pending", "Dewi")
		code, p := post(id, "complete", nil)
		assertProblem(t, code, p, 409, "illegal_transition", "status")
		errs := p["errors"].([]any)[0].(map[string]any)
		if errs["from"] != "pending" || errs["to"] != "completed" {
			t.Errorf("409 does not name from and to: %v", errs)
		}
	})

	t.Run("ship without a tracking number is a 422", func(t *testing.T) {
		id, _ := insertOrder(ctx, t, store, tenantID, "processing", "Sari")
		code, p := post(id, "ship", map[string]any{})
		assertProblem(t, code, p, 422, "validation_failed", "tracking_number")
	})

	t.Run("refund only on a cancelled paid order, once", func(t *testing.T) {
		id, _ := insertOrder(ctx, t, store, tenantID, "paid", "Putri")
		code, p := post(id, "refund", nil)
		assertProblem(t, code, p, 422, "validation_failed", "")

		if code, _ := post(id, "cancel", map[string]any{"reason": "Customer asked"}); code != 200 {
			t.Fatalf("cancel: %d", code)
		}
		code, o := post(id, "refund", map[string]any{"note": "BCA transfer"})
		if code != 200 || o["status"] != "cancelled" || o["refunded_at"] == nil {
			t.Fatalf("refund: %d %v", code, o)
		}
		code, p = post(id, "refund", nil)
		assertProblem(t, code, p, 422, "validation_failed", "")
	})

	t.Run("a viewer cannot transition", func(t *testing.T) {
		id, _ := insertOrder(ctx, t, store, tenantID, "pending", "Ayu")
		viewer := signInAnotherUser(ctx, t, store, tenantID, auth.RoleViewer)
		code, p := doWithHeaders(t, viewer, http.MethodPost, "/v1/orders/"+id+"/mark-paid", nil, nil)
		assertProblem(t, code, p, 403, "permission_denied", "")
	})
}
