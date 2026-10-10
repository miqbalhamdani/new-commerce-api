package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
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
		isolationCase{route: route{"GET", "/v1/customers"}, seed: seedCustomer,
			request: func(t *testing.T, s seeded) *http.Request {
				return bearerRequest(t, http.MethodGet, "/v1/customers?limit=200", s.accessToken)
			}},
		isolationCase{route: route{"GET", "/v1/customers/{id}"}, seed: seedCustomer,
			request: func(t *testing.T, s seeded) *http.Request {
				return bearerRequest(t, http.MethodGet, "/v1/customers/"+s.otherID, s.accessToken)
			}},
	)
}

// insertCustomer seeds one customer -- with a password hash, so a leak of the
// column would be caught -- and returns its id.
func insertCustomer(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID, name, email string) string {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO customers (id, tenant_id, email, name, phone, password_hash)
			VALUES ($1, $2, $3, $4, '+628555000111', 'argon2id$secret-hash-bytes')`, id, tenantID, email, name)
		return err
	}); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	return id.String()
}

// seedCustomer is a signed-in admin plus one customer whose name is the marker.
func seedCustomer(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID) seeded {
	t.Helper()
	s := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	s.marker = "Customer " + uuid.NewString()
	s.id = insertCustomer(ctx, t, store, tenantID, s.marker, "iso-"+uuid.NewString()+"@example.com")
	return s
}

// TestCustomers is P1-106's acceptance (04-api-spec.md §5.5; BR-092).
func TestCustomers(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	tenantID := uuid.Must(uuid.NewV7())
	admin := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
	do := apiClient(t, admin)

	rina := insertCustomer(ctx, t, store, tenantID, "Rina", "rina@example.com")
	insertCustomer(ctx, t, store, tenantID, "Dewi", "dewi@example.com")

	// Two of Rina's orders, placed apart so newest-first is observable.
	first, _ := insertOrder(ctx, t, store, tenantID, "completed", "Rina")
	second, _ := insertOrder(ctx, t, store, tenantID, "pending", "Rina")
	if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE orders SET customer_id = $1, source = 'storefront',
			placed_at = now() - interval '1 day' WHERE id = $2`, rina, first); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE orders SET customer_id = $1, source = 'storefront' WHERE id = $2`, rina, second)
		return err
	}); err != nil {
		t.Fatalf("attach orders: %v", err)
	}

	t.Run("list searches name, email and phone and counts orders", func(t *testing.T) {
		code, p := do("GET", "/v1/customers?q=rina", nil)
		if code != 200 {
			t.Fatalf("list: %d %v", code, p)
		}
		data := p["data"].([]any)
		if len(data) != 1 {
			t.Fatalf("%d rows, want 1", len(data))
		}
		row := data[0].(map[string]any)
		if row["name"] != "Rina" || row["order_count"] != float64(2) || row["email"] != "rina@example.com" {
			t.Errorf("row %v", row)
		}
		if got := do2(t, do, "/v1/customers?q=dewi%40example.com"); got != 1 {
			t.Errorf("email search found %d", got)
		}
		if got := do2(t, do, "/v1/customers?q=628555"); got != 2 {
			t.Errorf("phone search found %d", got)
		}
	})

	t.Run("the detail carries the orders, newest first", func(t *testing.T) {
		code, p := do("GET", "/v1/customers/"+rina, nil)
		if code != 200 {
			t.Fatalf("get: %d %v", code, p)
		}
		orders := p["orders"].([]any)
		if len(orders) != 2 {
			t.Fatalf("%d orders, want 2", len(orders))
		}
		if orders[0].(map[string]any)["id"] != second || orders[1].(map[string]any)["id"] != first {
			t.Errorf("not newest first: %v", orders)
		}
	})

	t.Run("no credential field ever appears", func(t *testing.T) {
		for _, path := range []string{"/v1/customers", "/v1/customers/" + rina} {
			rec := httptest.NewRecorder()
			newServer(t).ServeHTTP(rec, bearerRequest(t, http.MethodGet, path, admin.accessToken))
			body := rec.Body.String()
			if strings.Contains(body, "password") || strings.Contains(body, "secret-hash-bytes") {
				t.Errorf("%s leaks a credential field: %s", path, body)
			}
		}
	})

	t.Run("cursor pages cover every row once", func(t *testing.T) {
		var seen []string
		path := "/v1/customers?limit=1"
		for i := 0; path != "" && i < 10; i++ {
			_, p := do("GET", path, nil)
			for _, d := range p["data"].([]any) {
				seen = append(seen, d.(map[string]any)["id"].(string))
			}
			path = ""
			if c, ok := p["next_cursor"].(string); ok {
				path = "/v1/customers?limit=1&cursor=" + c
			}
		}
		if len(seen) != 2 || len(slices.Compact(slices.Sorted(slices.Values(seen)))) != 2 {
			t.Errorf("pages: %v", seen)
		}
	})

	t.Run("a viewer can read customers", func(t *testing.T) {
		viewer := signInAnotherUser(ctx, t, store, tenantID, auth.RoleViewer)
		code, _ := doWithHeaders(t, viewer, http.MethodGet, "/v1/customers", nil, nil)
		if code != 200 {
			t.Errorf("viewer list: %d", code)
		}
	})
}

func do2(t *testing.T, do func(string, string, any) (int, map[string]any), path string) int {
	t.Helper()
	code, p := do("GET", path, nil)
	if code != 200 {
		t.Fatalf("%s: %d", path, code)
	}
	return len(p["data"].([]any))
}
