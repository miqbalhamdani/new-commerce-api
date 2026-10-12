package storefront

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// isoCustomer is one shopper's things, each carrying their marker -- their
// name, email and the street of their order -- so a response that leaked
// them is found by searching its bytes. Their order number is a marker too:
// each shop has its own prefix.
type isoCustomer struct {
	marker, bearer, refresh, order, cart string
}

// isoShop is a tenant with two customers and two guest orders.
type isoShop struct {
	id         uuid.UUID
	shop       shop
	customers  [2]isoCustomer
	guestOrder [2]struct{ number, token, marker string }
}

func isoBuild(t *testing.T, name, prefix, origin string) *isoShop {
	t.Helper()
	store := openStore(t)
	tenantID, key := seedKey(t, store, origin)
	seedCatalog(t, store, tenantID)
	shipsFrom(t, store, tenantID)
	execTenant(t, store, tenantID, `UPDATE tenants SET order_prefix = '`+prefix+`'`)
	s := &isoShop{id: tenantID, shop: newShop(t, store, key)}
	full := variantBySKU(t, store, tenantID, "TS-BLK-M")
	checkout := func(marker, bearer string) (number, token string) {
		body := checkoutBody()
		body["contact"] = map[string]any{"name": marker, "email": marker + "@example.com"}
		body["shipping_address"].(map[string]any)["line1"] = "Jl. " + marker
		var headers []string
		if bearer != "" {
			headers = []string{"Authorization", "Bearer " + bearer}
		}
		code, o, raw := s.shop.do(t, "POST", "/v1/storefront/carts/"+cartWith(t, s.shop, map[string]int{full: 1})+"/checkout", body, headers...)
		if code != 201 {
			t.Fatalf("checkout for %s: %d %s", marker, code, raw)
		}
		token, _ = o["order_token"].(string)
		return o["order_number"].(string), token
	}
	for i := range s.customers {
		marker := "iso-" + name + "-c" + string(rune('1'+i)) + "-" + uuid.NewString()[:8]
		code, tok, raw := s.shop.do(t, "POST", "/v1/storefront/customers",
			map[string]any{"email": marker + "@example.com", "password": "correct-horse", "name": marker})
		if code != 201 {
			t.Fatalf("register %s: %d %s", marker, code, raw)
		}
		c := isoCustomer{marker: marker, bearer: tok["access_token"].(string), refresh: tok["refresh_token"].(string)}
		c.order, _ = checkout(marker, c.bearer)
		// A cart of theirs, claimed by signing in.
		_, cart, _ := s.shop.do(t, "POST", "/v1/storefront/carts", nil, "Authorization", "Bearer "+c.bearer)
		c.cart = cart["cart_id"].(string)
		s.customers[i] = c
	}
	for i := range s.guestOrder {
		marker := "iso-" + name + "-g" + string(rune('1'+i)) + "-" + uuid.NewString()[:8]
		s.guestOrder[i].marker = marker
		s.guestOrder[i].number, s.guestOrder[i].token = checkout(marker, "")
	}
	return s
}

// isoCase is one way a customer of one shop could reach another's data.
type isoCase struct {
	route string // "METHOD /pattern", as the router registers it
	// probe sends the request as me (customer 0 of mine), aimed at their own
	// things, and returns the status and the raw body.
	probe func(t *testing.T, mine, theirs *isoShop) (int, string)
	// own is the status the probe must answer with.
	own []int
	// shows is whether my own name or order number must be in the answer, so
	// a route whose answer carries no marker at all cannot pass vacuously.
	shows bool
}

var instance = regexp.MustCompile(`"instance":"[^"]*"`)

func bearer(c isoCustomer) []string { return []string{"Authorization", "Bearer " + c.bearer} }

var isoCases = []isoCase{
	{route: "GET /v1/storefront/me", own: []int{200}, shows: true,
		probe: func(t *testing.T, mine, _ *isoShop) (int, string) {
			code, _, raw := mine.shop.do(t, "GET", "/v1/storefront/me", nil, bearer(mine.customers[0])...)
			return code, raw
		}},
	{route: "PATCH /v1/storefront/me", own: []int{200}, shows: true,
		probe: func(t *testing.T, mine, _ *isoShop) (int, string) {
			code, _, raw := mine.shop.do(t, "PATCH", "/v1/storefront/me", map[string]any{"phone": "+6281111111111"}, bearer(mine.customers[0])...)
			return code, raw
		}},
	{route: "GET /v1/storefront/me/orders", own: []int{200}, shows: true,
		probe: func(t *testing.T, mine, _ *isoShop) (int, string) {
			code, _, raw := mine.shop.do(t, "GET", "/v1/storefront/me/orders", nil, bearer(mine.customers[0])...)
			return code, raw
		}},
	// My neighbour's order, by its number: the same 404 as none (BR-011).
	{route: "GET /v1/storefront/me/orders/{order_number}", own: []int{404},
		probe: func(t *testing.T, mine, _ *isoShop) (int, string) {
			code, _, raw := mine.shop.do(t, "GET", "/v1/storefront/me/orders/"+mine.customers[1].order, nil, bearer(mine.customers[0])...)
			return code, raw
		}},
	// A guest's token on another guest's order.
	{route: "GET /v1/storefront/orders/{order_number}", own: []int{401},
		probe: func(t *testing.T, mine, _ *isoShop) (int, string) {
			code, _, raw := mine.shop.do(t, "GET", "/v1/storefront/orders/"+mine.guestOrder[1].number, nil, "X-Order-Token", mine.guestOrder[0].token)
			return code, raw
		}},
	{route: "POST /v1/storefront/orders/{order_number}/payment", own: []int{404},
		probe: func(t *testing.T, mine, _ *isoShop) (int, string) {
			code, _, raw := mine.shop.do(t, "POST", "/v1/storefront/orders/"+mine.customers[1].order+"/payment", nil, bearer(mine.customers[0])...)
			return code, raw
		}},
	// My token with the other shop's key (BR-086).
	{route: "GET /v1/storefront/me", own: []int{401},
		probe: func(t *testing.T, mine, theirs *isoShop) (int, string) {
			code, _, raw := theirs.shop.do(t, "GET", "/v1/storefront/me", nil, bearer(mine.customers[0])...)
			return code, raw
		}},
	// My refresh token at the other shop.
	{route: "POST /v1/storefront/auth/refresh", own: []int{401},
		probe: func(t *testing.T, mine, theirs *isoShop) (int, string) {
			code, _, raw := theirs.shop.do(t, "POST", "/v1/storefront/auth/refresh", map[string]any{"refresh_token": mine.customers[0].refresh})
			return code, raw
		}},
	// Signing out with my token and my neighbour's refresh token signs no
	// one out; "my neighbour is still signed in" checks theirs still refreshes.
	{route: "POST /v1/storefront/auth/logout", own: []int{204},
		probe: func(t *testing.T, mine, _ *isoShop) (int, string) {
			code, _, raw := mine.shop.do(t, "POST", "/v1/storefront/auth/logout", map[string]any{"refresh_token": mine.customers[1].refresh}, bearer(mine.customers[0])...)
			return code, raw
		}},
	// The other shop's cart, by its id, with my key.
	{route: "GET /v1/storefront/carts/{cart_id}", own: []int{404},
		probe: func(t *testing.T, mine, theirs *isoShop) (int, string) {
			code, _, raw := mine.shop.do(t, "GET", "/v1/storefront/carts/"+theirs.customers[0].cart, nil, bearer(mine.customers[0])...)
			return code, raw
		}},
}

// TestCustomerIsolation is P1-211's acceptance: two shops, two customers each
// and two guests each, and zero leakage either way on every customer-scoped
// route (BR-001, BR-082). It extends P1-008's admin suite to the storefront:
// the same idea, a marker per shopper searched for in the raw response.
func TestCustomerIsolation(t *testing.T) {
	a := isoBuild(t, "a", "ISOA", "https://a.example")
	b := isoBuild(t, "b", "ISOB", "https://b.example")

	t.Run("every customer-scoped route has a case", func(t *testing.T) {
		for rt, need := range routeAccess {
			if need != customerOnly && need != orderAccess && rt != "POST /v1/storefront/auth/refresh" && rt != "GET /v1/storefront/carts/{cart_id}" {
				continue
			}
			covered := false
			for _, c := range isoCases {
				covered = covered || c.route == rt
			}
			if !covered {
				t.Errorf("%s has no isolation case -- add one to isoCases", rt)
			}
		}
	})

	for _, way := range []struct {
		name         string
		mine, theirs *isoShop
	}{{"a to b", a, b}, {"b to a", b, a}} {
		for _, c := range isoCases {
			t.Run(way.name+" "+c.route, func(t *testing.T) {
				code, raw := c.probe(t, way.mine, way.theirs)
				if !slices.Contains(c.own, code) {
					t.Errorf("status %d, want %v: %s", code, c.own, raw)
				}
				me := way.mine.customers[0]
				if c.shows && !strings.Contains(raw, me.marker) && !strings.Contains(raw, me.order) {
					t.Errorf("my own data is missing, so the leak check proves nothing: %s", raw)
				}
				// A problem's instance echoes the path I asked for, which may
				// name the order I aimed at; it is not data.
				raw = instance.ReplaceAllString(raw, "")
				// Nobody's marker but mine may appear.
				n, g := way.mine.customers[1], way.mine.guestOrder[1]
				others := []string{n.marker, n.order, g.marker, g.number}
				for _, cu := range way.theirs.customers {
					others = append(others, cu.marker, cu.order)
				}
				for _, g := range way.theirs.guestOrder {
					others = append(others, g.marker, g.number)
				}
				for _, m := range others {
					if strings.Contains(raw, m) {
						t.Errorf("leaked %s: %s", m, raw)
					}
				}
			})
		}
	}

	t.Run("my neighbour is still signed in", func(t *testing.T) {
		for _, s := range []*isoShop{a, b} {
			if code, _, raw := s.shop.do(t, "POST", "/v1/storefront/auth/refresh", map[string]any{"refresh_token": s.customers[1].refresh}); code != 200 {
				t.Errorf("neighbour's refresh after my logout with their token: %d %s", code, raw)
			}
		}
	})

	t.Run("a cart is never taken from its customer", func(t *testing.T) {
		// My token on my neighbour's cart: it stays theirs (BR-095).
		a.shop.do(t, "GET", "/v1/storefront/carts/"+a.customers[1].cart, nil, bearer(a.customers[0])...)
		var owner string
		ctx := context.WithoutCancel(t.Context())
		_ = openStore(t).InTenantTx(tenant.NewContext(ctx, a.id), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT c.name FROM carts k JOIN customers c ON c.id = k.customer_id WHERE k.id = $1`, a.customers[1].cart).Scan(&owner)
		})
		if owner != a.customers[1].marker {
			t.Errorf("cart owner %q, want %q", owner, a.customers[1].marker)
		}
	})
}
