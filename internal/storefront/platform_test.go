package storefront

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/miqbalhamdani/new-commerce-api/internal/team"
)

// TestPlatformKey is P1-701's acceptance (04-api-spec.md §11.2; BR-003,
// BR-140, BR-141): the hosted website's key reads the active shop X-Shop
// names, that shop only, and only on a GET from a server.
func TestPlatformKey(t *testing.T) {
	store := openStore(t)
	a, _ := seedKey(t, store, "https://a.test")
	b, _ := seedKey(t, store, "https://b.test")
	// tenants has no RLS: name the row.
	execTenant(t, store, a, `UPDATE tenants SET name = 'Platform Shop A' WHERE id = '`+a.String()+`'`)
	execTenant(t, store, b, `UPDATE tenants SET name = 'Platform Shop B' WHERE id = '`+b.String()+`'`)
	slugA, slugB := "sf-"+a.String(), "sf-"+b.String()

	const platform = "platform-test-key"
	keys := NewKeys(store)
	keys.Platform = platform
	clock := time.Now()
	keys.now = func() time.Time { return clock }
	h := NewRouter(NewServer(Services{Team: team.NewService(store, nil, testBox(t))}), keys, testSigner(t), nil)
	send := func(method, key, slug, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/v1/storefront/config", nil)
		r.Header.Set("X-Api-Key", key)
		if slug != "" {
			r.Header.Set("X-Shop", slug)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	for slug, names := range map[string][2]string{slugA: {"Platform Shop A", "Platform Shop B"}, slugB: {"Platform Shop B", "Platform Shop A"}} {
		rec := send("GET", platform, slug, "")
		if body := rec.Body.String(); rec.Code != 200 || !strings.Contains(body, names[0]) || strings.Contains(body, names[1]) {
			t.Errorf("%s: %d %s", slug, rec.Code, body)
		}
	}

	assertCode(t, "from a browser", send("GET", platform, slugA, "https://a.test"), 401, "invalid_api_key")
	assertCode(t, "a write", send("POST", platform, slugA, ""), 401, "invalid_api_key")
	assertCode(t, "unknown shop", send("GET", platform, "no-such-shop", ""), 404, "not_found")
	assertCode(t, "no shop", send("GET", platform, "", ""), 404, "not_found")

	// A suspended shop is gone once its cached slug expires (BR-085, BR-140).
	execTenant(t, store, a, `UPDATE tenants SET status = 'suspended' WHERE id = '`+a.String()+`'`)
	if rec := send("GET", platform, slugA, ""); rec.Code != 200 {
		t.Errorf("inside the cache window: %d", rec.Code)
	}
	clock = clock.Add(KeyCacheTTL + time.Second)
	assertCode(t, "suspended", send("GET", platform, slugA, ""), 404, "not_found")

	// With none configured, the platform key is nobody's key.
	keys.Platform = ""
	assertCode(t, "unconfigured", send("GET", platform, slugB, ""), 401, "invalid_api_key")
}
