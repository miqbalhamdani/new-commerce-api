package storefront

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miqbalhamdani/new-commerce-api/internal/ratelimit"
)

// countOnly is a window that never slides: it only counts, so a test can
// reach a limit without waiting a minute or sharing Redis with other tests.
type countOnly struct {
	mu sync.Mutex
	n  map[string]int
}

func (c *countOnly) SlidingWindow(_ context.Context, key string, limit int, window time.Duration) (bool, int, time.Duration, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n[key] >= limit {
		return false, c.n[key], window, nil
	}
	c.n[key]++
	return true, c.n[key], window, nil
}

// TestStorefrontLimits is P1-209's acceptance (BR-014): browser per IP and
// per key, server per key and IP, sign-in per email, checkout per IP, and the
// headers on every response.
func TestStorefrontLimits(t *testing.T) {
	store := openStore(t)
	tenantID, key := seedKey(t, store, "https://a.example")
	t.Cleanup(func() { cleanCatalog(t, store, tenantID) })
	limiter := ratelimit.New(&countOnly{n: map[string]int{}})
	s := newShop(t, store, key)
	h := NewRouter(NewServer(Services{Catalog: s.catalog(), Customers: s.cust, Limits: limiter}), NewKeys(store), s.signer, limiter)
	send := func(method, path, ip, origin, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = ip + ":40000"
		r.Header.Set("X-Api-Key", key)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	t.Run("a browser, per IP", func(t *testing.T) {
		for i := 1; i <= browserPerIP; i++ {
			rec := send("GET", "/v1/storefront/brands", "10.0.0.1", "https://a.example", "")
			if rec.Code != 200 || rec.Header().Get("RateLimit-Remaining") == "" ||
				!strings.Contains(rec.Header().Get("Access-Control-Expose-Headers"), "RateLimit-Remaining") {
				t.Fatalf("request %d: %d %v", i, rec.Code, rec.Header())
			}
		}
		rec := send("GET", "/v1/storefront/brands", "10.0.0.1", "https://a.example", "")
		if rec.Code != 429 || rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), "rate_limited") {
			t.Errorf("over: %d %v", rec.Code, rec.Header())
		}
		// Another address behind the same key is its own budget.
		if rec := send("GET", "/v1/storefront/brands", "10.0.0.2", "https://a.example", ""); rec.Code != 200 {
			t.Errorf("another IP: %d", rec.Code)
		}
	})

	t.Run("the website's server, per key and IP", func(t *testing.T) {
		for i := 1; i <= serverPerKey; i++ {
			if rec := send("GET", "/v1/storefront/brands", "10.0.1.1", "", ""); rec.Code != 200 {
				t.Fatalf("request %d: %d", i, rec.Code)
			}
		}
		if rec := send("GET", "/v1/storefront/brands", "10.0.1.1", "", ""); rec.Code != 429 {
			t.Errorf("over: %d", rec.Code)
		}
	})

	t.Run("checkout, per IP", func(t *testing.T) {
		for i := 1; i <= checkoutPerIP; i++ {
			// The cart does not exist: still counted, still a checkout.
			if rec := send("POST", "/v1/storefront/carts/7c1e2f0a-0000-4000-8000-000000000000/checkout", "10.0.2.1", "", "{}"); rec.Code == 429 {
				t.Fatalf("checkout %d refused early", i)
			}
		}
		if rec := send("POST", "/v1/storefront/carts/7c1e2f0a-0000-4000-8000-000000000000/checkout", "10.0.2.1", "", "{}"); rec.Code != 429 {
			t.Errorf("checkout 11: %d", rec.Code)
		}
	})

	t.Run("sign-in, per email", func(t *testing.T) {
		body := `{"email":"Rina@Example.com","password":"wrong-password"}`
		for i := 1; i <= ratelimit.LoginPerEmail; i++ {
			if rec := send("POST", "/v1/storefront/auth/login", "10.0.3.1", "", body); rec.Code != 401 {
				t.Fatalf("login %d: %d", i, rec.Code)
			}
		}
		rec := send("POST", "/v1/storefront/auth/login", "10.0.3.9", "", `{"email":"rina@example.com","password":"wrong-password"}`)
		if rec.Code != 429 {
			t.Errorf("login 11 from another IP, same email: %d", rec.Code)
		}
		if rec := send("POST", "/v1/storefront/auth/password-reset", "10.0.3.9", "", `{"email":"rina@example.com","reset_url":"https://a.example/r"}`); rec.Code != 429 {
			t.Errorf("a reset for the same email: %d, want it to share the login budget", rec.Code)
		}
	})
}
