package storefront

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// access is what a storefront route needs beyond the API key (BR-082).
type access int

const (
	public       access = iota // the key alone: catalog, config, carts, checkout
	signsInRoute               // issues tokens, so ignores a stale one
	customerOnly               // a customer token for the key's shop
	orderAccess                // the order's token, or (pay-again) its customer
)

// routeAccess classifies every storefront route. A route missing here fails
// TestStorefrontAuthMatrix: a new route is classified before it ships.
var routeAccess = map[string]access{
	"GET /v1/storefront/products":                           public,
	"GET /v1/storefront/products/{slug}":                    public,
	"GET /v1/storefront/categories":                         public,
	"GET /v1/storefront/brands":                             public,
	"GET /v1/storefront/config":                             public,
	"POST /v1/storefront/carts":                             public,
	"GET /v1/storefront/carts/{cart_id}":                    public,
	"PUT /v1/storefront/carts/{cart_id}/items/{variant_id}": public,
	"POST /v1/storefront/carts/{cart_id}/shipping-rates":    public,
	"POST /v1/storefront/carts/{cart_id}/checkout":          public,
	"POST /v1/storefront/customers":                         signsInRoute,
	"POST /v1/storefront/auth/login":                        signsInRoute,
	"POST /v1/storefront/auth/google":                       signsInRoute,
	"POST /v1/storefront/auth/refresh":                      signsInRoute,
	"POST /v1/storefront/auth/password-reset":               signsInRoute,
	"POST /v1/storefront/auth/password-reset/confirm":       signsInRoute,
	"POST /v1/storefront/auth/logout":                       customerOnly,
	"GET /v1/storefront/me":                                 customerOnly,
	"PATCH /v1/storefront/me":                               customerOnly,
	"GET /v1/storefront/me/orders":                          customerOnly,
	"GET /v1/storefront/me/orders/{order_number}":           customerOnly,
	"GET /v1/storefront/orders/{order_number}":              orderAccess,
	"POST /v1/storefront/orders/{order_number}/payment":     orderAccess,
}

// storefrontRoutes is every route the generated storefront router registers,
// as "METHOD /pattern".
func storefrontRoutes(t *testing.T) []string {
	t.Helper()
	r := chi.NewRouter()
	HandlerFromMuxWithBaseURL(&Server{}, r, BaseURL)
	var out []string
	if err := chi.Walk(r, func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		out = append(out, method+" "+pattern)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// concrete fills a route pattern's parameters with ids that parse.
func concrete(pattern string) string {
	return strings.NewReplacer("{slug}", "no-such-product", "{cart_id}", uuid.NewString(),
		"{variant_id}", uuid.NewString(), "{order_number}", "SF-000001").Replace(pattern)
}

// TestStorefrontAuthMatrix is P1-210's acceptance: every storefront route,
// against every origin and every kind of bearer token (BR-082, BR-083,
// BR-085, BR-086). It asserts the credential checks only -- who gets past
// them -- not each route's own answer, which its own test owns.
func TestStorefrontAuthMatrix(t *testing.T) {
	store := openStore(t)
	tenantA, keyA := seedKey(t, store, "https://a.example")
	tenantB, keyB := seedKey(t, store, "https://b.example")
	t.Cleanup(func() { cleanCatalog(t, store, tenantA); cleanCatalog(t, store, tenantB) })
	a := newShop(t, store, keyA)
	b := newShop(t, store, keyB)

	routes := storefrontRoutes(t)
	t.Run("every route is classified", func(t *testing.T) {
		for _, r := range routes {
			if _, ok := routeAccess[r]; !ok {
				t.Errorf("%s has no entry in routeAccess -- classify it", r)
			}
		}
		if len(routes) != len(routeAccess) {
			t.Errorf("%d routes registered, %d classified: routeAccess lists a route that no longer exists", len(routes), len(routeAccess))
		}
	})

	customer := signUp(t, a, "matrix@example.com")["access_token"].(string)
	otherShop := signUp(t, b, "matrix@example.com")["access_token"].(string)
	staff, _ := a.signer.Issue(uuid.New(), tenantA, "owner", time.Now())
	tokens := map[string]string{
		"no token":                "",
		"a customer":              customer,
		"another shop's customer": otherShop,
		"a staff token":           staff,
		"garbage":                 "not-a-token",
	}
	origins := map[string]string{"allowed": "https://a.example", "foreign": "https://evil.example", "absent": ""}

	checked := 0
	t.Cleanup(func() { t.Logf("%d requests across %d routes", checked, len(routes)) })
	for _, rt := range routes {
		method, pattern, _ := strings.Cut(rt, " ")
		need := routeAccess[rt]
		for originName, origin := range origins {
			for tokenName, token := range tokens {
				r := httptest.NewRequest(method, concrete(pattern), strings.NewReader("{}"))
				r.Header.Set("X-Api-Key", keyA)
				r.Header.Set("Content-Type", "application/json")
				if origin != "" {
					r.Header.Set("Origin", origin)
				}
				if token != "" {
					r.Header.Set("Authorization", "Bearer "+token)
				}
				rec := httptest.NewRecorder()
				a.h.ServeHTTP(rec, r)
				checked++
				var p struct{ Type string }
				_ = json.Unmarshal(rec.Body.Bytes(), &p)
				code := p.Type[strings.LastIndex(p.Type, "/")+1:]
				acao := rec.Header().Get("Access-Control-Allow-Origin")
				what := rt + " · origin " + originName + " · " + tokenName

				badToken := tokenName != "no token" && tokenName != "a customer"
				var want string
				switch {
				case originName == "foreign":
					want = "origin_not_allowed" // BR-083, before any token is read
				case badToken && need != signsInRoute:
					want = "customer_auth_required" // BR-086: never treated as a guest
				case tokenName == "no token" && (need == customerOnly || need == orderAccess):
					want = "customer_auth_required" // BR-082: not with a key alone
				}
				switch {
				case want != "" && (rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden || code != want):
					t.Errorf("%s: %d %s, want %s", what, rec.Code, code, want)
				case want == "" && (code == "customer_auth_required" && need != orderAccess || code == "origin_not_allowed" || code == "invalid_api_key"):
					t.Errorf("%s: %d %s, want past the credential checks", what, rec.Code, code)
				}
				// CORS: the key's own website gets its origin back, nobody
				// else ever does (BR-083).
				if (originName == "allowed") != (acao == origin && acao != "") {
					t.Errorf("%s: Access-Control-Allow-Origin %q", what, acao)
				}
			}
		}
		// No key, or a revoked one, is 401 invalid_api_key whatever else is sent.
		r := httptest.NewRequest(method, concrete(pattern), strings.NewReader("{}"))
		r.Header.Set("Authorization", "Bearer "+customer)
		rec := httptest.NewRecorder()
		a.h.ServeHTTP(rec, r)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "invalid_api_key") {
			t.Errorf("%s with no key: %d %s", rt, rec.Code, rec.Body)
		}
	}
}
