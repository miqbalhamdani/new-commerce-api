package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	httpapi "github.com/miqbalhamdani/new-commerce-api/internal/http"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/email"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/team"
)

func init() {
	// The answer is an empty 202 whatever the email, so there is nothing of
	// another tenant's that could come back.
	isolationCases = append(isolationCases,
		isolationCase{route: route{"POST", "/v1/auth/register"}, seed: seedAuthUser,
			request: func(t *testing.T, s seeded) *http.Request {
				return jsonRequest(t, "/v1/auth/register", map[string]any{"shop_name": "Iso", "name": "Iso", "email": s.email})
			}},
		// P1-086: a refused code, an empty 202, a client ID, and Google off in
		// the suite's server -- none of them carries a tenant's rows.
		isolationCase{route: route{"POST", "/v1/auth/verify-code"}, seed: seedAuthUser,
			request: func(t *testing.T, s seeded) *http.Request {
				return jsonRequest(t, "/v1/auth/verify-code", map[string]any{"email": s.email, "code": "000000"})
			}},
		isolationCase{route: route{"POST", "/v1/auth/resend-code"}, seed: seedAuthUser,
			request: func(t *testing.T, s seeded) *http.Request {
				return jsonRequest(t, "/v1/auth/resend-code", map[string]any{"email": s.email})
			}},
		isolationCase{route: route{"GET", "/v1/auth/providers"}, seed: seedAuthUser,
			request: func(t *testing.T, s seeded) *http.Request {
				return httptest.NewRequest(http.MethodGet, "/v1/auth/providers", nil)
			}},
		isolationCase{route: route{"POST", "/v1/auth/google"}, seed: seedAuthUser,
			request: func(t *testing.T, s seeded) *http.Request {
				return jsonRequest(t, "/v1/auth/google", map[string]any{"id_token": "not-a-token"})
			}},
		isolationCase{route: route{"POST", "/v1/auth/register/google"}, seed: seedAuthUser,
			request: func(t *testing.T, s seeded) *http.Request {
				return jsonRequest(t, "/v1/auth/register/google", map[string]any{"id_token": "not-a-token", "shop_name": "Iso"})
			}},
	)
}

// signupOwner is what the database owner sees of a signup: the API has no
// route that returns another tenant's rows.
type signupOwner struct {
	tenantID, userID        uuid.UUID
	status, role            string
	hasPassword             bool
	shop, slug, orderPrefix string
	audits                  int
}

func readSignup(t *testing.T, owner *pgx.Conn, address string) (signupOwner, bool) {
	t.Helper()
	var s signupOwner
	err := owner.QueryRow(t.Context(), `
		SELECT u.tenant_id, u.id, u.status, u.role, u.password_hash IS NOT NULL, t.name, t.slug, t.order_prefix,
		       (SELECT count(*) FROM audit_log a WHERE a.tenant_id = t.id AND a.action = 'tenant.register' AND a.actor_id = u.id)
		FROM users u JOIN tenants t ON t.id = u.tenant_id WHERE u.email = $1`, address).
		Scan(&s.tenantID, &s.userID, &s.status, &s.role, &s.hasPassword, &s.shop, &s.slug, &s.orderPrefix, &s.audits)
	if err == pgx.ErrNoRows {
		return s, false
	}
	if err != nil {
		t.Fatalf("read signup: %v", err)
	}
	return s, true
}

// cleanupSignup removes a signup's shop when t ends: best effort, as the
// other seeds.
func cleanupSignup(t *testing.T, owner *pgx.Conn, tenantID uuid.UUID) {
	t.Cleanup(func() {
		bg := context.WithoutCancel(t.Context())
		_, _ = owner.Exec(bg, `DELETE FROM refresh_tokens WHERE tenant_id = $1`, tenantID)
		_, _ = owner.Exec(bg, `DELETE FROM users WHERE tenant_id = $1`, tenantID)
		_, _ = owner.Exec(bg, `DELETE FROM tenants WHERE id = $1`, tenantID)
	})
}

// TestRegister is P1-084's acceptance (04-api-spec.md §2; BR-003, BR-020,
// BR-021, BR-026): always 202; a new email gets a shop and an invited owner
// whose link signs them in; registering again resends only while unconfirmed.
func TestRegister(t *testing.T) {
	ctx := t.Context()
	owner, err := pgx.Connect(ctx, config.DatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.WithoutCancel(ctx)) })
	redis := testRedis(t)
	emails := func() int64 { n, _ := redis.Len(ctx, email.Stream); return n }
	register := func(body map[string]any) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		newServer(t).ServeHTTP(rec, jsonRequest(t, "/v1/auth/register", body))
		return rec
	}

	address := "budi-" + uuid.NewString() + "@erigo.co.id"
	before := emails()
	if rec := register(map[string]any{"shop_name": "Erigo Store", "name": "Budi", "email": address}); rec.Code != 202 || rec.Body.Len() != 0 {
		t.Fatalf("register: %d %s", rec.Code, rec.Body)
	}
	s, ok := readSignup(t, owner, address)
	if !ok {
		t.Fatal("no owner was created")
	}
	cleanupSignup(t, owner, s.tenantID)
	if s.status != "invited" || s.role != "owner" || s.hasPassword || s.shop != "Erigo Store" || s.orderPrefix != "ERI" ||
		!regexp.MustCompile(`^erigo-store-[0-9a-f]{6}$`).MatchString(s.slug) || s.audits != 1 {
		t.Fatalf("signup: %+v", s)
	}
	if emails() != before+1 {
		t.Error("the confirmation was not queued after commit")
	}

	t.Run("registering again before confirming resends the link, and opens no second shop", func(t *testing.T) {
		before := emails()
		if rec := register(map[string]any{"shop_name": "Erigo Two", "name": "Budi", "email": strings.ToUpper(address)}); rec.Code != 202 {
			t.Fatalf("again: %d %s", rec.Code, rec.Body)
		}
		if again, _ := readSignup(t, owner, address); again.tenantID != s.tenantID || again.shop != "Erigo Store" {
			t.Errorf("a second register changed the shop: %+v", again)
		}
		if emails() != before+1 {
			t.Error("the link was not sent again")
		}
	})

	t.Run("the link signs the owner in, and then registering sends nothing", func(t *testing.T) {
		rec := httptest.NewRecorder()
		newServer(t).ServeHTTP(rec, jsonRequest(t, "/v1/auth/accept-invite",
			map[string]any{"token": tokenFor(t, seeded{}, s.userID.String()), "password": "budi-password"}))
		var session struct {
			User   struct{ Role string }
			Tenant struct{ ID string }
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &session)
		if rec.Code != 200 || session.User.Role != "owner" || session.Tenant.ID != s.tenantID.String() {
			t.Fatalf("accept: %d %s", rec.Code, rec.Body)
		}
		before := emails()
		if rec := register(map[string]any{"shop_name": "Erigo", "name": "Budi", "email": address}); rec.Code != 202 {
			t.Fatalf("after confirming: %d", rec.Code)
		}
		if emails() != before {
			t.Error("an active owner was sent a signup link")
		}
	})

	t.Run("bad input is 422 on the field", func(t *testing.T) {
		for field, body := range map[string]map[string]any{
			"shop_name": {"shop_name": "  ", "name": "Budi", "email": "a-" + uuid.NewString() + "@example.com"},
			"name":      {"shop_name": "Shop", "name": strings.Repeat("x", 101), "email": "b-" + uuid.NewString() + "@example.com"},
		} {
			rec := register(body)
			var p map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &p)
			assertProblem(t, rec.Code, p, 422, "validation_failed", field)
		}
		rec := register(map[string]any{"shop_name": "Shop", "name": "Budi", "email": "not-an-email"})
		if rec.Code != 422 {
			t.Errorf("bad email: %d %s", rec.Code, rec.Body)
		}
	})
}

// TestSignupLimit is BR-014's login row for signup: ten per email in 15
// minutes, counted before anything touches the database.
func TestSignupLimit(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	signer, err := auth.NewSigner(isoSigningKey)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := pgx.Connect(ctx, config.DatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.WithoutCancel(ctx)) })
	limits := httpapi.NewRateLimiter(countingOnly{n: map[string]int{}}, httpapi.AdminRateLimit, httpapi.AdminRateWindow)
	h := httpapi.NewRouter(httpapi.NewServer(httpapi.Services{Team: team.NewService(store, testRedis(t), nil), Limits: limits}, false), signer, limits)
	address := "limit-" + uuid.NewString() + "@example.com"
	register := func() int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, jsonRequest(t, "/v1/auth/register", map[string]any{"shop_name": "Limit", "name": "L", "email": address}))
		return rec.Code
	}
	for i := 1; i <= 10; i++ {
		if code := register(); code != 202 {
			t.Fatalf("try %d: %d", i, code)
		}
	}
	if s, ok := readSignup(t, owner, address); ok {
		cleanupSignup(t, owner, s.tenantID)
	}
	if code := register(); code != http.StatusTooManyRequests {
		t.Errorf("try 11: %d, want 429", code)
	}
}

// TestSignupCode is P1-086's email code (04-api-spec.md §2; BR-014, BR-026):
// the code in the signup email buys the invitation token, which sets the
// password; a wrong code, or one after the owner is active, is 422 on code;
// resending is always 202 and mails only an unconfirmed signup.
func TestSignupCode(t *testing.T) {
	ctx := t.Context()
	owner, err := pgx.Connect(ctx, config.DatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.WithoutCancel(ctx)) })
	redis := testRedis(t)
	emails := func() int64 { n, _ := redis.Len(ctx, email.Stream); return n }
	post := func(path string, body map[string]any) (*httptest.ResponseRecorder, map[string]any) {
		rec := httptest.NewRecorder()
		newServer(t).ServeHTTP(rec, jsonRequest(t, path, body))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec, out
	}

	address := "code-" + uuid.NewString() + "@erigo.co.id"
	if rec, _ := post("/v1/auth/register", map[string]any{"shop_name": "Erigo", "name": "Budi", "email": address}); rec.Code != 202 {
		t.Fatalf("register: %d", rec.Code)
	}
	s, ok := readSignup(t, owner, address)
	if !ok {
		t.Fatal("no signup")
	}
	cleanupSignup(t, owner, s.tenantID)
	code := testInvites(t).Code(s.userID, s.tenantID, time.Now())

	t.Run("a wrong code is 422 on code", func(t *testing.T) {
		wrong := "000000"
		if code == wrong {
			wrong = "111111"
		}
		rec, p := post("/v1/auth/verify-code", map[string]any{"email": address, "code": wrong})
		assertProblem(t, rec.Code, p, 422, "validation_failed", "code")
	})
	t.Run("resending mails the unconfirmed signup, and answers 202 for anyone", func(t *testing.T) {
		before := emails()
		if rec, _ := post("/v1/auth/resend-code", map[string]any{"email": strings.ToUpper(address)}); rec.Code != 202 {
			t.Fatalf("resend: %d", rec.Code)
		}
		if emails() != before+1 {
			t.Error("the code was not sent again")
		}
		before = emails()
		if rec, _ := post("/v1/auth/resend-code", map[string]any{"email": "nobody-" + uuid.NewString() + "@example.com"}); rec.Code != 202 || emails() != before {
			t.Errorf("an unknown email: %d, queued %d", rec.Code, emails()-before)
		}
	})
	t.Run("the code buys the token, the token sets the password and signs the owner in", func(t *testing.T) {
		rec, out := post("/v1/auth/verify-code", map[string]any{"email": address, "code": code})
		token, _ := out["token"].(string)
		if rec.Code != 200 || !strings.HasPrefix(token, "inv_") {
			t.Fatalf("verify: %d %v", rec.Code, out)
		}
		rec, out = post("/v1/auth/accept-invite", map[string]any{"token": token, "password": "budi-password"})
		if user, _ := out["user"].(map[string]any); rec.Code != 200 || user["role"] != "owner" {
			t.Fatalf("accept: %d %v", rec.Code, out)
		}
		rec, p := post("/v1/auth/verify-code", map[string]any{"email": address, "code": code})
		assertProblem(t, rec.Code, p, 422, "validation_failed", "code")
	})
}

// TestCodeLimit is BR-014 for codes: the eleventh guess for one email in 15
// minutes is 429, before the code is looked at.
func TestCodeLimit(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	signer, err := auth.NewSigner(isoSigningKey)
	if err != nil {
		t.Fatal(err)
	}
	limits := httpapi.NewRateLimiter(countingOnly{n: map[string]int{}}, httpapi.AdminRateLimit, httpapi.AdminRateWindow)
	h := httpapi.NewRouter(httpapi.NewServer(httpapi.Services{Auth: auth.NewService(store, signer), Invites: testInvites(t), Limits: limits}, false), signer, limits)
	address := "guess-" + uuid.NewString() + "@example.com"
	guess := func() int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, jsonRequest(t, "/v1/auth/verify-code", map[string]any{"email": address, "code": "123456"}))
		return rec.Code
	}
	for i := 1; i <= 10; i++ {
		if code := guess(); code != 422 {
			t.Fatalf("guess %d: %d", i, code)
		}
	}
	if code := guess(); code != http.StatusTooManyRequests {
		t.Errorf("guess 11: %d, want 429", code)
	}
}
