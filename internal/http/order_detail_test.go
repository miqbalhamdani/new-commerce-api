package httpapi_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

func init() {
	isolationCases = append(isolationCases,
		isolationCase{route: route{"GET", "/v1/orders/{id}"}, seed: seedOrderAt("pending"),
			request: func(t *testing.T, s seeded) *http.Request {
				return bearerRequest(t, http.MethodGet, "/v1/orders/"+s.otherID, s.accessToken)
			}},
		isolationCase{route: route{"PATCH", "/v1/orders/{id}"}, seed: seedOrderAt("pending"),
			request: func(t *testing.T, s seeded) *http.Request {
				r := bodyRequest(t, http.MethodPatch, "/v1/orders/"+s.otherID, s.accessToken, map[string]any{"note": "Mine"})
				r.Header.Set("If-Match", "1")
				return r
			}},
	)
	auditCases = append(auditCases,
		auditCase{route: route{"PATCH", "/v1/orders/{id}"},
			request: func(t *testing.T, s seeded) (*http.Request, string, string) {
				ctx := t.Context()
				store := openAppStore(ctx, t)
				id, _ := insertOrder(ctx, t, store, uuid.MustParse(s.tenantID), "pending", "Audit patch")
				r := bodyRequest(t, http.MethodPatch, "/v1/orders/"+id, s.accessToken, map[string]any{"note": "Changed"})
				r.Header.Set("If-Match", "1")
				return r, "order", id
			}},
	)
}

// TestOrderDetail is P1-104's acceptance (04-api-spec.md §5.2; BR-010,
// BR-079): the detail shape, and PATCH of exactly three fields while pending.
func TestOrderDetail(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	tenantID := uuid.Must(uuid.NewV7())
	admin := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	do := apiClient(t, admin)
	patch := func(id string, version string, body map[string]any) (int, map[string]any) {
		t.Helper()
		return doWithHeaders(t, admin, http.MethodPatch, "/v1/orders/"+id, body, map[string]string{"If-Match": version})
	}

	id, _ := insertOrder(ctx, t, store, tenantID, "pending", "Rina")

	t.Run("the detail carries every field, payments empty", func(t *testing.T) {
		code, o := do("GET", "/v1/orders/"+id, nil)
		if code != 200 {
			t.Fatalf("get: %d %v", code, o)
		}
		for _, k := range []string{"id", "order_number", "source", "status", "version", "customer_id",
			"customer", "shipping_address", "note", "lines", "subtotal", "shipping", "discount", "total",
			"payment_method", "payments", "shipping_courier", "shipping_service", "courier",
			"tracking_number", "placed_at", "paid_at", "shipped_at", "completed_at", "cancelled_at",
			"refunded_at", "allowed_transitions", "history"} {
			if _, ok := o[k]; !ok {
				t.Errorf("detail is missing %q", k)
			}
		}
		if ps := o["payments"].([]any); len(ps) != 0 {
			t.Errorf("payments %v, want []", ps)
		}
		at := o["allowed_transitions"].([]any)
		if len(at) != 2 || at[0] != "paid" || at[1] != "cancelled" {
			t.Errorf("allowed_transitions %v, want [paid cancelled]", at)
		}
	})

	t.Run("PATCH edits address, note and shipping and recomputes the total", func(t *testing.T) {
		code, o := patch(id, "1", map[string]any{
			"shipping_address": map[string]any{"line1": "Jl. Melati 12", "city": "Bandung",
				"province": "Jawa Barat", "postal_code": "40115"},
			"note": "Bungkus kado", "shipping": 1500000})
		if code != 200 || o["version"] != float64(2) || o["shipping"] != float64(1500000) ||
			o["total"] != float64(1500000) || o["note"] != "Bungkus kado" {
			t.Fatalf("patch: %d %v", code, o)
		}
		if addr := o["shipping_address"].(map[string]any); addr["city"] != "Bandung" || addr["line2"] != nil {
			t.Errorf("address %v", addr)
		}

		code, o = patch(id, "2", map[string]any{"note": nil})
		if code != 200 || o["note"] != nil || o["version"] != float64(3) {
			t.Errorf("clearing the note: %d %v", code, o)
		}
	})

	t.Run("anything else is refused", func(t *testing.T) {
		code, p := patch(id, "3", map[string]any{"status": "paid"})
		assertProblem(t, code, p, 422, "validation_failed", "status")
		code, p = patch(id, "3", map[string]any{"total": 1})
		assertProblem(t, code, p, 422, "unknown_field", "total")
	})

	t.Run("a stale If-Match is 409, a missing one 422", func(t *testing.T) {
		code, p := patch(id, "1", map[string]any{"note": "stale"})
		assertProblem(t, code, p, 409, "version_conflict", "version")
		code, p = doWithHeaders(t, admin, http.MethodPatch, "/v1/orders/"+id, map[string]any{"note": "x"}, nil)
		assertProblem(t, code, p, 422, "validation_failed", "If-Match")
	})

	t.Run("only a pending order can be edited", func(t *testing.T) {
		paidID, _ := insertOrder(ctx, t, store, tenantID, "paid", "Dewi")
		code, p := patch(paidID, "1", map[string]any{"note": "too late"})
		assertProblem(t, code, p, 422, "validation_failed", "")
	})
}

// TestOrderDetailPayments is P1-220's admin half: an order's Midtrans
// attempts, newest first (§5.2, BR-123).
func TestOrderDetailPayments(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	tenantID := uuid.Must(uuid.NewV7())
	admin := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	id, _ := insertOrder(ctx, t, store, tenantID, "pending", "Pay")
	if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO payments (id, tenant_id, order_id, provider_order_id, attempt, status, gross_amount, expires_at, transaction_status)
			SELECT gen_random_uuid(), tenant_id, id, order_number, 1, 'failed', total_amount, now(), 'expire' FROM orders WHERE id = $1`, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO payments (id, tenant_id, order_id, provider_order_id, attempt, gross_amount, expires_at)
			SELECT gen_random_uuid(), tenant_id, id, order_number || '-2', 2, total_amount, now() + interval '1 day' FROM orders WHERE id = $1`, id)
		return err
	}); err != nil {
		t.Fatalf("seed payments: %v", err)
	}
	code, o := apiClient(t, admin)("GET", "/v1/orders/"+id, nil)
	pays, _ := o["payments"].([]any)
	if code != 200 || len(pays) != 2 {
		t.Fatalf("detail: %d %v", code, o)
	}
	first, second := pays[0].(map[string]any), pays[1].(map[string]any)
	if !strings.HasSuffix(first["provider_order_id"].(string), "-2") || first["status"] != "pending" ||
		second["status"] != "failed" || second["transaction_status"] != "expire" {
		t.Errorf("payments %v, want the -2 attempt first", pays)
	}
}
