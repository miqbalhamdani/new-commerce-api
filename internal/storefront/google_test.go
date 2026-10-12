package storefront

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// fakeGoogle is Google for a test: a JWKS for one key, and ID tokens signed
// with it.
type fakeGoogle struct {
	key *rsa.PrivateKey
	srv *httptest.Server
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	g := &fakeGoogle{key: key}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kid": "k1", "kty": "RSA",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGoogle) idToken(t *testing.T, aud, sub, email string, verified bool) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"iss": "accounts.google.com", "aud": aud, "sub": sub,
		"email": email, "email_verified": verified, "name": "Rina Google", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()})
	tok.Header["kid"] = "k1"
	s, err := tok.SignedString(g.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestGoogleSignIn is P1-223's acceptance (04-api-spec.md §11.8; BR-127).
func TestGoogleSignIn(t *testing.T) {
	ctx := t.Context()
	store := openStore(t)
	tenantID, key := seedKey(t, store, "https://a.example")
	t.Cleanup(func() { cleanCatalog(t, store, tenantID) })
	a := newShop(t, store, key)
	g := newFakeGoogle(t)
	a.cust.Google = &auth.GoogleVerifier{CertsURL: g.srv.URL, HTTP: g.srv.Client()}
	const client = "shop-a.apps.googleusercontent.com"
	google := func(idToken string) (int, map[string]any) {
		code, body, _ := a.do(t, "POST", "/v1/storefront/auth/google", map[string]any{"id_token": idToken})
		return code, body
	}

	code, body := google(g.idToken(t, client, "g-1", "rina@example.com", true))
	assertProblem(t, "a shop without a client ID", code, body, 422, "validation_failed", "id_token")
	execTenant(t, store, tenantID, `INSERT INTO storefront_settings (tenant_id, midtrans_webhook_id, google_client_id)
		VALUES (current_setting('app.tenant_id')::uuid, gen_random_uuid(), '`+client+`')`)

	t.Run("refused", func(t *testing.T) {
		for what, tok := range map[string]string{
			"another app's audience": g.idToken(t, "someone-else", "g-1", "rina@example.com", true),
			"an unverified email":    g.idToken(t, client, "g-1", "rina@example.com", false),
			"a forged token":         g.idToken(t, client, "g-1", "rina@example.com", true)[:40] + "xx",
		} {
			code, body := google(tok)
			assertProblem(t, what, code, body, 401, "unauthenticated", "")
		}
	})

	// A new customer, with no password; then the same one again.
	code, first := google(g.idToken(t, client, "g-new", "Gita@Example.com", true))
	if code != 201 || first["customer"].(map[string]any)["email"] != "gita@example.com" || first["customer"].(map[string]any)["name"] != "Rina Google" {
		t.Fatalf("first sign-in: %d %v", code, first)
	}
	code, again := google(g.idToken(t, client, "g-new", "gita@example.com", true))
	if code != 200 || again["customer"].(map[string]any)["id"] != first["customer"].(map[string]any)["id"] {
		t.Errorf("returning: %d %v", code, again)
	}
	if code, _, _ := a.do(t, "POST", "/v1/storefront/auth/login", map[string]any{"email": "gita@example.com", "password": "anything-at-all"}); code != 401 {
		t.Errorf("a Google-only customer signed in with a password: %d", code)
	}

	// An existing account with the same verified email is linked, not doubled.
	rina := signUp(t, a, "rina@example.com")
	code, linked := google(g.idToken(t, client, "g-rina", "rina@example.com", true))
	if code != 200 || linked["customer"].(map[string]any)["id"] != rina["customer"].(map[string]any)["id"] {
		t.Errorf("link: %d %v", code, linked)
	}
	// That account already has its Google login: a second Google account with
	// the same email is refused, not linked as well.
	code, body = google(g.idToken(t, client, "g-rina-2", "rina@example.com", true))
	assertProblem(t, "a second Google account", code, body, 401, "unauthenticated", "")

	var customers, identities int
	_ = store.InTenantTx(tenant.NewContext(context.WithoutCancel(ctx), tenantID), func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM customers`).Scan(&customers); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM customer_identities`).Scan(&identities)
	})
	if customers != 2 || identities != 2 {
		t.Errorf("%d customers and %d logins, want 2 and 2: none duplicated", customers, identities)
	}
}
