package storefront

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/customers"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/email"
)

// resetLink asks for a reset for address and renders the mail the worker
// would send, returning the link's token.
func resetLink(t *testing.T, s shop, store *db.Store, address string) string {
	t.Helper()
	before := len(s.mail.all())
	code, _, raw := s.do(t, "POST", "/v1/storefront/auth/password-reset",
		map[string]any{"email": address, "reset_url": "https://a.example/reset-password?lang=id"})
	if code != 202 {
		t.Fatalf("request reset: %d %s", code, raw)
	}
	sent := s.mail.all()
	if len(sent) != before+1 {
		t.Fatalf("%d envelopes queued, want one more than %d", len(sent), before)
	}
	msg, err := customers.ResetMail(store, s.resets)(t.Context(), sent[len(sent)-1])
	if err != nil || msg == nil {
		t.Fatalf("render: %v %v", msg, err)
	}
	i := strings.Index(msg.Text, "https://a.example/reset-password?")
	link, err := url.Parse(strings.Fields(msg.Text[i:])[0])
	if err != nil || link.Query().Get("lang") != "id" {
		t.Fatalf("link in %q: %v", msg.Text, err)
	}
	return link.Query().Get("token")
}

// TestPasswordReset is P1-207's acceptance (04-api-spec.md §11.8; BR-021,
// BR-094): always 202, a link valid 30 minutes and once, that ends every
// session, and adds a password for a Google-only customer.
func TestPasswordReset(t *testing.T) {
	store := openStore(t)
	tenantA, keyA := seedKey(t, store, "https://a.example")
	tenantB, keyB := seedKey(t, store, "https://b.example")
	t.Cleanup(func() { cleanCatalog(t, store, tenantA); cleanCatalog(t, store, tenantB) })
	a, b := newShop(t, store, keyA), newShop(t, store, keyB)
	tok := signUp(t, a, "rina@example.com")

	t.Run("always 202, mail only for an account", func(t *testing.T) {
		code, _, _ := a.do(t, "POST", "/v1/storefront/auth/password-reset",
			map[string]any{"email": "nobody@example.com", "reset_url": "https://a.example/reset"})
		if code != 202 || len(a.mail.all()) != 0 {
			t.Errorf("unknown email: %d, %d envelopes", code, len(a.mail.all()))
		}
		code, _, _ = a.do(t, "POST", "/v1/storefront/auth/password-reset",
			map[string]any{"email": " RINA@example.com", "reset_url": "https://a.example/reset"})
		sent := a.mail.all()
		if code != 202 || len(sent) != 1 || sent[0].Kind != "password_reset" || sent[0].TenantID != tenantA ||
			sent[0].URL != "https://a.example/reset" {
			t.Errorf("account: %d %+v", code, sent)
		}
	})

	t.Run("the link is for the key's own website", func(t *testing.T) {
		for _, u := range []string{"https://a.example.evil.test/reset", "https://evil.test/reset",
			"http://a.example/reset", "https://user@a.example/reset", "/reset", "not a url"} {
			code, body, _ := a.do(t, "POST", "/v1/storefront/auth/password-reset",
				map[string]any{"email": "rina@example.com", "reset_url": u})
			assertProblem(t, u, code, body, 422, "validation_failed", "reset_url")
		}
	})

	t.Run("a link sets the password once and signs out everywhere", func(t *testing.T) {
		first := resetLink(t, a, store, "rina@example.com")
		second := resetLink(t, a, store, "rina@example.com")
		if !strings.HasPrefix(first, "prt_") {
			t.Fatalf("token %q", first)
		}

		code, body, _ := b.do(t, "POST", "/v1/storefront/auth/password-reset/confirm", map[string]any{"token": first, "password": "a-new-password"})
		assertProblem(t, "another shop's key", code, body, 422, "validation_failed", "token")
		code, body, _ = a.do(t, "POST", "/v1/storefront/auth/password-reset/confirm", map[string]any{"token": first, "password": "short"})
		assertProblem(t, "short password", code, body, 422, "validation_failed", "password")

		if code, _, raw := a.do(t, "POST", "/v1/storefront/auth/password-reset/confirm",
			map[string]any{"token": first, "password": "a-new-password"}); code != 204 {
			t.Fatalf("confirm: %d %s", code, raw)
		}
		for what, token := range map[string]string{"the same link": first, "an earlier link": second} {
			code, body, _ := a.do(t, "POST", "/v1/storefront/auth/password-reset/confirm", map[string]any{"token": token, "password": "yet-another-one"})
			assertProblem(t, what, code, body, 422, "validation_failed", "token")
		}
		code, body, _ = a.do(t, "POST", "/v1/storefront/auth/refresh", map[string]any{"refresh_token": tok["refresh_token"]})
		assertProblem(t, "a session from before", code, body, 401, "unauthenticated", "")
		if code, _, _ := a.do(t, "POST", "/v1/storefront/auth/login", map[string]any{"email": "rina@example.com", "password": "a-new-password"}); code != 200 {
			t.Errorf("login with the new password: %d", code)
		}
	})

	t.Run("two confirms of one link: exactly one sets it", func(t *testing.T) {
		link := resetLink(t, a, store, "rina@example.com")
		codes := make([]int, 2)
		var wg sync.WaitGroup
		for i := range codes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes[i], _, _ = a.do(t, "POST", "/v1/storefront/auth/password-reset/confirm",
					map[string]any{"token": link, "password": "racing-password-" + string(rune('a'+i))})
			}()
		}
		wg.Wait()
		if codes[0]+codes[1] != 204+422 {
			t.Errorf("codes %v, want one 204 and one 422", codes)
		}
	})

	t.Run("an expired link", func(t *testing.T) {
		old := a.resets.Mint(uuid.New(), tenantA, nil, time.Now().Add(-31*time.Minute))
		code, body, _ := a.do(t, "POST", "/v1/storefront/auth/password-reset/confirm", map[string]any{"token": old, "password": "a-new-password"})
		assertProblem(t, "expired", code, body, 422, "validation_failed", "token")
	})

	t.Run("a Google-only customer can add a password", func(t *testing.T) {
		execTenant(t, store, tenantA, `INSERT INTO customers (id, tenant_id, email, name)
			VALUES (gen_random_uuid(), current_setting('app.tenant_id')::uuid, 'google@example.com', 'Gita')`)
		link := resetLink(t, a, store, "google@example.com")
		if code, _, raw := a.do(t, "POST", "/v1/storefront/auth/password-reset/confirm",
			map[string]any{"token": link, "password": "first-password"}); code != 204 {
			t.Fatalf("confirm: %d %s", code, raw)
		}
		if code, _, _ := a.do(t, "POST", "/v1/storefront/auth/login", map[string]any{"email": "google@example.com", "password": "first-password"}); code != 200 {
			t.Errorf("login: %d", code)
		}
	})

	t.Run("an account gone before the worker runs gets no mail", func(t *testing.T) {
		msg, err := customers.ResetMail(store, a.resets)(context.WithoutCancel(t.Context()),
			email.Envelope{Kind: "password_reset", TenantID: tenantA, CustomerID: uuid.New(), URL: "https://a.example/reset"})
		if msg != nil || err != nil {
			t.Errorf("render for no customer: %v %v", msg, err)
		}
	})
}
