package storefront

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// signUp registers a customer at s and returns the tokens.
func signUp(t *testing.T, s shop, email string) map[string]any {
	t.Helper()
	code, tok, raw := s.do(t, "POST", "/v1/storefront/customers",
		map[string]any{"email": email, "password": "correct-horse", "name": "Rina", "phone": "+6281234567890"})
	if code != 201 {
		t.Fatalf("register %s: %d %s", email, code, raw)
	}
	return tok
}

// TestCustomerAccounts is P1-206's acceptance (04-api-spec.md §11.8; BR-021,
// BR-086, BR-092, BR-093).
func TestCustomerAccounts(t *testing.T) {
	store := openStore(t)
	tenantA, keyA := seedKey(t, store, "https://a.example")
	tenantB, keyB := seedKey(t, store, "https://b.example")
	t.Cleanup(func() { cleanCatalog(t, store, tenantA); cleanCatalog(t, store, tenantB) })
	a, b := newShop(t, store, keyA), newShop(t, store, keyB)

	tok := signUp(t, a, " Rina@Example.com ")
	refresh := tok["refresh_token"].(string)
	if !strings.HasPrefix(refresh, "rt_") || tok["expires_in"] != float64(900) || tok["access_token"] == "" ||
		tok["customer"].(map[string]any)["email"] != "rina@example.com" {
		t.Fatalf("register: %v", tok)
	}

	t.Run("an email is one account per shop", func(t *testing.T) {
		code, body, _ := a.do(t, "POST", "/v1/storefront/customers",
			map[string]any{"email": "RINA@example.com", "password": "another-one", "name": "Rina 2"})
		assertProblem(t, "same shop", code, body, 422, "validation_failed", "email")
		signUp(t, b, "rina@example.com") // another shop: unrelated (BR-092)

		for what, req := range map[string]struct {
			body  map[string]any
			field string
		}{
			"short password": {map[string]any{"email": "x@example.com", "password": "short", "name": "X"}, "password"},
			"not an email":   {map[string]any{"email": "nope", "password": "long-enough", "name": "X"}, "email"},
			"no name":        {map[string]any{"email": "y@example.com", "password": "long-enough", "name": " "}, "name"},
		} {
			code, body, _ := a.do(t, "POST", "/v1/storefront/customers", req.body)
			assertProblem(t, what, code, body, 422, "validation_failed", req.field)
		}
		code, body, _ = a.do(t, "POST", "/v1/storefront/customers",
			map[string]any{"email": "z@example.com", "password": "long-enough", "name": "Z", "password_hash": "x"})
		assertProblem(t, "a hash", code, body, 422, "unknown_field", "password_hash")
	})

	t.Run("every failed sign-in is one answer", func(t *testing.T) {
		execTenant(t, store, tenantA, `INSERT INTO customers (id, tenant_id, email, name)
			VALUES (gen_random_uuid(), current_setting('app.tenant_id')::uuid, 'google-only@example.com', 'G')`)
		for what, creds := range map[string][2]string{
			"wrong password": {"rina@example.com", "wrong-horse"},
			"unknown email":  {"nobody@example.com", "correct-horse"},
			"no password":    {"google-only@example.com", "correct-horse"},
			"another shop's": {"rina@example.com", "x"},
		} {
			code, body, _ := a.do(t, "POST", "/v1/storefront/auth/login", map[string]any{"email": creds[0], "password": creds[1]})
			assertProblem(t, what, code, body, 401, "unauthenticated", "")
		}
		code, tok, raw := a.do(t, "POST", "/v1/storefront/auth/login",
			map[string]any{"email": "RINA@example.com", "password": "correct-horse"},
			"Authorization", "Bearer a-stale-token") // ignored on sign-in
		if code != 200 || tok["customer"].(map[string]any)["name"] != "Rina" {
			t.Errorf("login: %d %s", code, raw)
		}
	})

	t.Run("refresh rotates, and a reused token ends the session", func(t *testing.T) {
		code, next, raw := a.do(t, "POST", "/v1/storefront/auth/refresh", map[string]any{"refresh_token": refresh})
		rotated, _ := next["refresh_token"].(string)
		if code != 200 || rotated == "" || rotated == refresh {
			t.Fatalf("refresh: %d %s", code, raw)
		}
		code, body, _ := a.do(t, "POST", "/v1/storefront/auth/refresh", map[string]any{"refresh_token": refresh})
		assertProblem(t, "reused", code, body, 401, "unauthenticated", "")
		code, body, _ = a.do(t, "POST", "/v1/storefront/auth/refresh", map[string]any{"refresh_token": rotated})
		assertProblem(t, "after reuse, the rotated one too", code, body, 401, "unauthenticated", "")
		// And another shop's key cannot use a token from this one.
		_, fresh, _ := a.do(t, "POST", "/v1/storefront/auth/login", map[string]any{"email": "rina@example.com", "password": "correct-horse"})
		code, body, _ = b.do(t, "POST", "/v1/storefront/auth/refresh", map[string]any{"refresh_token": fresh["refresh_token"]})
		assertProblem(t, "another shop's refresh token", code, body, 401, "unauthenticated", "")
	})

	t.Run("logout needs the customer, and ends that session", func(t *testing.T) {
		_, s, _ := a.do(t, "POST", "/v1/storefront/auth/login", map[string]any{"email": "rina@example.com", "password": "correct-horse"})
		bearer := "Bearer " + s["access_token"].(string)
		body := map[string]any{"refresh_token": s["refresh_token"]}

		code, p, _ := a.do(t, "POST", "/v1/storefront/auth/logout", body)
		assertProblem(t, "no token", code, p, 401, "customer_auth_required", "")

		staff, _ := a.signer.Issue(uuid.New(), tenantA, "owner", time.Now())
		code, p, _ = a.do(t, "POST", "/v1/storefront/auth/logout", body, "Authorization", "Bearer "+staff)
		assertProblem(t, "a staff token (BR-086)", code, p, 401, "customer_auth_required", "")

		code, p, _ = b.do(t, "POST", "/v1/storefront/auth/logout", body, "Authorization", bearer)
		assertProblem(t, "tenant B's key with tenant A's token (BR-086)", code, p, 401, "customer_auth_required", "")

		if code, _, raw := a.do(t, "POST", "/v1/storefront/auth/logout", body, "Authorization", bearer); code != 204 {
			t.Fatalf("logout: %d %s", code, raw)
		}
		code, p, _ = a.do(t, "POST", "/v1/storefront/auth/refresh", body)
		assertProblem(t, "refresh after logout", code, p, 401, "unauthenticated", "")
	})

	t.Run("a customer token on a catalog route is fine, an invalid one is not", func(t *testing.T) {
		if code, _, _ := a.do(t, "GET", "/v1/storefront/brands", nil, "Authorization", "Bearer "+tok["access_token"].(string)); code != 200 {
			t.Errorf("valid token on a guest route: %d", code)
		}
		code, p, _ := a.do(t, "GET", "/v1/storefront/brands", nil, "Authorization", "Bearer forged")
		assertProblem(t, "forged token", code, p, 401, "customer_auth_required", "")
	})
}
