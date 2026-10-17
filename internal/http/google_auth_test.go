package httpapi_test

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
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	httpapi "github.com/miqbalhamdani/new-commerce-api/internal/http"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/team"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

const adminClient = "admin-client.apps.googleusercontent.com"

// fakeGoogle serves a JWKS for one key and signs ID tokens with it, as the
// storefront's tests do: the test is Google.
type fakeGoogle struct {
	server *httptest.Server
	key    *rsa.PrivateKey
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	g := &fakeGoogle{key: key}
	g.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "k1", "kty": "RSA", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(g.server.Close)
	return g
}

func (g *fakeGoogle) token(t *testing.T, aud, email, name string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": "https://accounts.google.com", "aud": aud, "sub": "g-" + uuid.NewString(), "email": email,
		"email_verified": true, "name": name, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()})
	tok.Header["kid"] = "k1"
	s, err := tok.SignedString(g.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestGoogleAuth is P1-086's staff Google (04-api-spec.md §2; BR-020): the
// client ID is published, an active user signs in, an invited one is
// accepted and signed in, nobody and a disabled user are 401, and signing up
// opens a shop with an active owner and no password, once.
func TestGoogleAuth(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	owner, err := pgx.Connect(ctx, config.DatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.WithoutCancel(ctx)) })
	signer, err := auth.NewSigner(isoSigningKey)
	if err != nil {
		t.Fatal(err)
	}
	g := newFakeGoogle(t)
	h := httpapi.NewRouter(httpapi.NewServer(httpapi.Services{
		Auth: auth.NewService(store, signer), Team: team.NewService(store, testRedis(t), nil), Invites: testInvites(t),
		Google: &auth.GoogleVerifier{CertsURL: g.server.URL, HTTP: g.server.Client()}, GoogleClientID: adminClient,
	}, false), signer, nil)
	do := func(method, path string, body map[string]any) (int, map[string]any) {
		var r *http.Request
		if body == nil {
			r = httptest.NewRequest(method, path, nil)
		} else {
			r = jsonRequest(t, path, body)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	signIn := func(email string) (int, map[string]any) {
		return do("POST", "/v1/auth/google", map[string]any{"id_token": g.token(t, adminClient, email, "")})
	}

	if code, out := do("GET", "/v1/auth/providers", nil); code != 200 || out["google_client_id"] != adminClient {
		t.Fatalf("providers: %d %v", code, out)
	}

	t.Run("an active user signs in", func(t *testing.T) {
		tenantID := uuid.Must(uuid.NewV7())
		s := seedAuthUser(ctx, t, store, tenantID)
		code, out := signIn(s.email)
		if tn, _ := out["tenant"].(map[string]any); code != 200 || tn["id"] != tenantID.String() {
			t.Fatalf("sign in: %d %v", code, out)
		}
	})
	t.Run("an invited user is accepted, then signed in", func(t *testing.T) {
		tenantID := uuid.Must(uuid.NewV7())
		s := seedInvited(ctx, t, store, tenantID)
		if code, out := signIn(s.marker); code != 200 {
			t.Fatalf("sign in: %d %v", code, out)
		}
		var status string
		var audits int
		if err := owner.QueryRow(ctx, `SELECT u.status, (SELECT count(*) FROM audit_log a WHERE a.tenant_id = u.tenant_id
			AND a.action = 'user.accept_invite' AND a.subject_id = u.id::text AND a.after->>'via' = 'google')
			FROM users u WHERE u.id = $1`, s.id).Scan(&status, &audits); err != nil {
			t.Fatal(err)
		}
		if status != "active" || audits != 1 {
			t.Errorf("status %q, %d audit rows", status, audits)
		}
	})
	t.Run("no account and a disabled user are 401, saying which", func(t *testing.T) {
		code, p := signIn("nobody-" + uuid.NewString() + "@example.com")
		assertProblem(t, code, p, 401, "unauthenticated", "")
		if p["detail"] != "No account uses this Google email. Sign up to open a shop." {
			t.Errorf("no account: %v", p["detail"])
		}
		tenantID := uuid.Must(uuid.NewV7())
		s := seedAuthUser(ctx, t, store, tenantID)
		if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE users SET status = 'disabled' WHERE email = $1`, s.email)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		code, p = signIn(s.email)
		assertProblem(t, code, p, 401, "unauthenticated", "")
		if p["detail"] != "Google could not confirm this sign-in." {
			t.Errorf("disabled: %v", p["detail"])
		}
	})
	t.Run("a token for another client is 401", func(t *testing.T) {
		code, p := do("POST", "/v1/auth/google", map[string]any{"id_token": g.token(t, "someone-else", "x@example.com", "")})
		assertProblem(t, code, p, 401, "unauthenticated", "")
	})
	t.Run("signing up opens a shop with an active owner and no password, once", func(t *testing.T) {
		address := "gsignup-" + uuid.NewString() + "@erigo.co.id"
		code, out := do("POST", "/v1/auth/register/google", map[string]any{
			"id_token": g.token(t, adminClient, address, "Budi Santoso"), "shop_name": "Erigo Google"})
		if user, _ := out["user"].(map[string]any); code != 201 || user["role"] != "owner" || user["name"] != "Budi Santoso" {
			t.Fatalf("register: %d %v", code, out)
		}
		s, ok := readSignup(t, owner, address)
		if !ok {
			t.Fatal("no owner")
		}
		cleanupSignup(t, owner, s.tenantID)
		if s.status != "active" || s.hasPassword || s.shop != "Erigo Google" || s.audits != 1 {
			t.Errorf("signup: %+v", s)
		}
		code, p := do("POST", "/v1/auth/register/google", map[string]any{
			"id_token": g.token(t, adminClient, address, "Budi"), "shop_name": "Again"})
		assertProblem(t, code, p, 422, "validation_failed", "email")
		code, p = do("POST", "/v1/auth/register/google", map[string]any{
			"id_token": g.token(t, adminClient, "x-"+uuid.NewString()+"@example.com", ""), "shop_name": " "})
		assertProblem(t, code, p, 422, "validation_failed", "shop_name")
	})
	t.Run("with no client ID, Google is not set up", func(t *testing.T) {
		off := httpapi.NewRouter(httpapi.NewServer(httpapi.Services{Auth: auth.NewService(store, signer)}, false), signer, nil)
		rec := httptest.NewRecorder()
		off.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/auth/providers", nil))
		if rec.Body.String() != "{\"google_client_id\":null}\n" {
			t.Errorf("providers off: %s", rec.Body)
		}
		rec = httptest.NewRecorder()
		off.ServeHTTP(rec, jsonRequest(t, "/v1/auth/google", map[string]any{"id_token": "x"}))
		var p map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		assertProblem(t, rec.Code, p, 422, "validation_failed", "id_token")
	})
}
