package httpapi_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

func init() {
	isolationCases = append(isolationCases,
		// A's owner aiming at B's customer: 404, and B's name nowhere.
		// TestCustomerPurge checks B's row is untouched.
		isolationCase{route: route{"POST", "/v1/customers/{id}/purge"},
			seed: func(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID) seeded {
				s := seedCustomer(ctx, t, store, tenantID)
				owner := signInAnotherUser(ctx, t, store, tenantID, auth.RoleOwner)
				s.accessToken = owner.accessToken
				return s
			},
			request: func(t *testing.T, s seeded) *http.Request {
				return bearerRequest(t, http.MethodPost, "/v1/customers/"+s.otherID+"/purge", s.accessToken)
			}},
	)
	auditCases = append(auditCases,
		auditCase{route: route{"POST", "/v1/customers/{id}/purge"},
			request: func(t *testing.T, s seeded) (*http.Request, string, string) {
				ctx := t.Context()
				id := insertCustomer(ctx, t, openAppStore(ctx, t), uuid.MustParse(s.tenantID), "Audit", "audit-"+uuid.NewString()+"@example.com")
				return bearerRequest(t, http.MethodPost, "/v1/customers/"+id+"/purge", s.accessToken), "job", ""
			}},
	)
}

// TestCustomerPurge is P1-215's acceptance (04-api-spec.md §5.5; BR-097):
// the owner erases one shopper's personal data, from their account, their
// orders -- guest ones placed with their email included -- and the audit rows
// that recorded it, and nothing of anyone else's.
func TestCustomerPurge(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	tenantID := uuid.Must(uuid.NewV7())
	owner := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleOwner)
	admin := signInAnotherUser(ctx, t, store, tenantID, auth.RoleAdmin)
	do := apiClient(t, owner)

	rina := insertCustomer(ctx, t, store, tenantID, "Rina", "rina@example.com")
	dewi := insertCustomer(ctx, t, store, tenantID, "Dewi", "dewi@example.com")
	own, _ := insertOrder(ctx, t, store, tenantID, "pending", "Rina")
	guest, _ := insertOrder(ctx, t, store, tenantID, "completed", "Rina")
	other, _ := insertOrder(ctx, t, store, tenantID, "pending", "Dewi")
	exec := func(sql string, args ...any) {
		t.Helper()
		if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, sql, args...)
			return err
		}); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	address := `{"line1": "Jl. Melati 12", "line2": "Blok C", "city": "Bandung", "province": "Jawa Barat", "postal_code": "40115"}`
	exec(`UPDATE orders SET customer_id = $1, source = 'storefront', note = 'Call Rina on +628555',
		customer = '{"name": "Rina", "email": "rina@example.com", "phone": "+628555000111"}', shipping_address = $2
		WHERE id = $3`, rina, address, own)
	exec(`UPDATE orders SET customer = '{"name": "Rina", "email": "Rina@Example.com", "phone": null}', shipping_address = $1
		WHERE id = $2`, address, guest)
	dewiAddress := `{"line1": "Jl. Kenanga 5", "line2": null, "city": "Bogor", "province": "Jawa Barat", "postal_code": "16111"}`
	exec(`UPDATE orders SET customer_id = $1, customer = '{"name": "Dewi", "email": "dewi@example.com", "phone": null}',
		shipping_address = $2 WHERE id = $3`, dewi, dewiAddress, other)
	exec(`INSERT INTO customer_identities (tenant_id, customer_id, provider, subject) VALUES ($1, $2, 'google', $3)`,
		tenantID, rina, "g-"+uuid.NewString())
	exec(`INSERT INTO customer_sessions (id, tenant_id, customer_id, refresh_token_hash, expires_at)
		VALUES ($1, $2, $3, $4, now() + interval '1 day')`, uuid.Must(uuid.NewV7()), tenantID, rina, uuid.NewString())
	exec(`INSERT INTO payments (id, tenant_id, order_id, provider_order_id, attempt, gross_amount, expires_at, last_notification)
		VALUES ($1, $2, $3, $4, 1, 100, now(), '{"masked_card": "4811-11xx-xxxx-1114"}')`,
		uuid.Must(uuid.NewV7()), tenantID, own, "P-"+uuid.NewString())
	exec(`INSERT INTO audit_log (tenant_id, action, subject_type, subject_id, before, after) VALUES
		($1, 'order.update', 'order', $2, jsonb_build_object('shipping_address', $3::jsonb, 'shipping', 0),
		 '{"shipping_address": {"line1": "Jl. Mawar 3"}, "shipping": 1000}'),
		($1, 'order.update', 'order', $4, jsonb_build_object('shipping_address', $5::jsonb), '{"note": "Dewi keeps this"}')`,
		tenantID, own, address, other, dewiAddress)

	t.Run("only the owner may", func(t *testing.T) {
		code, p := apiClient(t, admin)("POST", "/v1/customers/"+rina+"/purge", nil)
		assertProblem(t, code, p, 403, "permission_denied", "")
		if d, _ := p["detail"].(string); !strings.Contains(d, "customers:purge") {
			t.Errorf("403 detail %q does not name customers:purge (BR-024)", d)
		}
		code, p = do("POST", "/v1/customers/"+uuid.NewString()+"/purge", nil)
		assertProblem(t, code, p, 404, "not_found", "")
	})

	purge := func() map[string]any {
		t.Helper()
		code, accepted := do("POST", "/v1/customers/"+rina+"/purge", nil)
		if code != 202 {
			t.Fatalf("purge: %d %v", code, accepted)
		}
		job := waitForJob(t, owner, accepted["job_id"].(string))
		if job["state"] != "done" || job["kind"] != "customer_purge" {
			t.Fatalf("job %v", job)
		}
		return job["result"].(map[string]any)
	}
	if r := purge(); r["orders"] != float64(2) {
		t.Errorf("result %v, want 2 orders: Rina's own and her guest one", r)
	}

	_, c := do("GET", "/v1/customers/"+rina, nil)
	if c["name"] != "Deleted customer" || c["email"] != "purged+"+rina+"@invalid" || c["phone"] != nil || c["purged_at"] == nil {
		t.Errorf("customer after purge: %v", c)
	}
	if _, d := do("GET", "/v1/customers/"+dewi, nil); d["name"] != "Dewi" || d["purged_at"] != nil {
		t.Errorf("Dewi was touched: %v", d)
	}

	var everything string
	if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM customer_identities WHERE customer_id = $1)
			+ (SELECT count(*) FROM customer_sessions WHERE customer_id = $1)
			+ (SELECT count(*) FROM payments WHERE last_notification IS NOT NULL)
			+ (SELECT count(*) FROM customers WHERE id = $1 AND password_hash IS NOT NULL)`, rina).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Errorf("%d identities, sessions, notifications or password hashes left", n)
		}
		for _, id := range []string{own, guest} {
			var addr string
			if err := tx.QueryRow(ctx, `SELECT shipping_address::text FROM orders WHERE id = $1 AND note IS NULL
				AND customer = '{"name": "Deleted customer", "email": null, "phone": null}'`, id).Scan(&addr); err != nil {
				t.Errorf("order %s not redacted: %v", id, err)
			}
			if addr != `{"city": "Bandung", "line1": "", "line2": null, "province": "Jawa Barat", "postal_code": "40115"}` {
				t.Errorf("order %s address %s", id, addr)
			}
		}
		// Every place an order or its audit trail could keep her data.
		return tx.QueryRow(ctx, `SELECT string_agg(format('%s %s %s', o.customer, o.shipping_address, o.note), ' ')
			|| (SELECT string_agg(format('[%s] %s %s', a.action, a.before, a.after), ' ') FROM audit_log a)
			FROM orders o`).Scan(&everything)
	}); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"rina@example.com", "Rina@Example.com", "+628555", "Melati", "Mawar", "Blok C", "4811"} {
		if strings.Contains(everything, gone) {
			t.Errorf("%q survived the purge", gone)
		}
	}
	for _, kept := range []string{"Dewi keeps this", "dewi@example.com", "Kenanga", `"shipping": 1000`} {
		if !strings.Contains(everything, kept) {
			t.Errorf("%q was lost, but is not Rina's personal data", kept)
		}
	}
	if !strings.Contains(everything, `[customer.purge]  {"orders": 2}`) { // no before, no actor
		t.Error("no customer.purge audit row")
	}

	// Again: nothing left to erase, and nothing breaks.
	if r := purge(); r["orders"] != float64(1) {
		t.Errorf("second purge result %v, want her own order only", r)
	}
}
