package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	// P1-088: an empty 202 and a refused token -- neither carries a
	// tenant's rows.
	isolationCases = append(isolationCases,
		isolationCase{route: route{"POST", "/v1/auth/password-reset"}, seed: seedAuthUser,
			request: func(t *testing.T, s seeded) *http.Request {
				return jsonRequest(t, "/v1/auth/password-reset", map[string]any{"email": s.email})
			}},
		isolationCase{route: route{"POST", "/v1/auth/password-reset/confirm"}, seed: seedAuthUser,
			request: func(t *testing.T, s seeded) *http.Request {
				return jsonRequest(t, "/v1/auth/password-reset/confirm", map[string]any{"token": "prt_x.y", "password": "long-enough"})
			}},
	)
	auditCases = append(auditCases,
		auditCase{route: route{"POST", "/v1/auth/password-reset/confirm"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			u := staffUser(t, s.email)
			return jsonRequest(t, "/v1/auth/password-reset/confirm", map[string]any{"token": u.reset(t, time.Now()), "password": "long-enough"}),
				"user", u.id.String()
		}},
	)
}

// testResets signs reset tokens with the development key, as the worker that
// mints them does; Staff() is the admin's.
func testResets(t *testing.T) *auth.ResetTokens {
	t.Helper()
	secret, err := config.PasswordResetSecret()
	if err != nil {
		t.Fatal(err)
	}
	r, err := auth.NewResetTokens(secret)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// resetUser is what a reset token is minted from, read as the owner.
type resetUser struct {
	id, tenantID uuid.UUID
	hash         *string
}

func staffUser(t *testing.T, address string) resetUser {
	t.Helper()
	owner, err := pgx.Connect(t.Context(), config.DatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close(t.Context()) }()
	var u resetUser
	if err := owner.QueryRow(t.Context(), `SELECT id, tenant_id, password_hash FROM users WHERE email = $1`, address).
		Scan(&u.id, &u.tenantID, &u.hash); err != nil {
		t.Fatalf("read user: %v", err)
	}
	return u
}

// reset is the link the worker would mail now.
func (u resetUser) reset(t *testing.T, now time.Time) string {
	return testResets(t).Staff().Mint(u.id, u.tenantID, u.hash, now)
}

// TestStaffPasswordReset is P1-088's acceptance (04-api-spec.md §2; BR-021,
// BR-094): asking is 202 for anyone and mails only an active user; the link
// sets the password once, signs the user out everywhere and in again; a
// Google-only owner adds a password this way; a customer's token, an expired
// one and a short password are 422.
func TestStaffPasswordReset(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	owner, err := pgx.Connect(ctx, config.DatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.WithoutCancel(ctx)) })
	redis := testRedis(t)
	emails := func() int64 { n, _ := redis.Len(ctx, email.Stream); return n }
	srv := newServer(t)
	post := func(path string, body map[string]any, cookies ...*http.Cookie) (*httptest.ResponseRecorder, map[string]any) {
		r := jsonRequest(t, path, body)
		for _, c := range cookies {
			r.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec, out
	}
	ask := func(address string) int64 {
		t.Helper()
		before := emails()
		if rec, _ := post("/v1/auth/password-reset", map[string]any{"email": address}); rec.Code != 202 || rec.Body.Len() != 0 {
			t.Fatalf("ask %s: %d %s", address, rec.Code, rec.Body)
		}
		return emails() - before
	}

	t.Run("asking is 202 for anyone, and mails only an active user", func(t *testing.T) {
		active := seedAuthUser(ctx, t, store, uuid.Must(uuid.NewV7()))
		if n := ask(active.email); n != 1 {
			t.Errorf("active: %d emails, want 1", n)
		}
		invited := seedInvited(ctx, t, store, uuid.Must(uuid.NewV7()))
		disabled := seedAuthUser(ctx, t, store, uuid.Must(uuid.NewV7()))
		if _, err := owner.Exec(ctx, `UPDATE users SET status = 'disabled' WHERE email = $1`, disabled.email); err != nil {
			t.Fatal(err)
		}
		for what, address := range map[string]string{
			"unknown": "nobody-" + uuid.NewString() + "@example.com", "invited": invited.marker, "disabled": disabled.email,
		} {
			if n := ask(address); n != 0 {
				t.Errorf("%s: %d emails, want 0", what, n)
			}
		}
	})

	t.Run("a Google-only owner adds a password, once, and is signed out elsewhere", func(t *testing.T) {
		tenantID := uuid.Must(uuid.NewV7())
		s := seedAuthUser(ctx, t, store, tenantID)
		if _, err := owner.Exec(ctx, `UPDATE users SET password_hash = NULL, role = 'owner' WHERE email = $1`, s.email); err != nil {
			t.Fatal(err)
		}
		u := staffUser(t, s.email)
		signer, err := auth.NewSigner(isoSigningKey)
		if err != nil {
			t.Fatal(err)
		}
		// Signed in with Google on another device.
		other, err := auth.NewService(store, signer).SessionFor(ctx, tenantID, u.id, auth.RoleOwner)
		if err != nil {
			t.Fatal(err)
		}
		token := u.reset(t, time.Now())

		rec, out := post("/v1/auth/password-reset/confirm", map[string]any{"token": token, "password": "owner-password"})
		if tn, _ := out["tenant"].(map[string]any); rec.Code != 200 || tn["id"] != tenantID.String() || len(rec.Result().Cookies()) == 0 {
			t.Fatalf("confirm: %d %v", rec.Code, out)
		}
		if rec, out := post("/v1/auth/login", map[string]any{"email": s.email, "password": "owner-password"}); rec.Code != 200 {
			t.Errorf("login with the new password: %d %v", rec.Code, out)
		}
		rec, p := post("/v1/auth/password-reset/confirm", map[string]any{"token": token, "password": "again-password"})
		assertProblem(t, rec.Code, p, 422, "validation_failed", "token")
		if rec, _ := post("/v1/auth/refresh", nil, &http.Cookie{Name: "refresh_token", Value: other.RefreshToken}); rec.Code != 401 {
			t.Errorf("the other device still refreshes: %d", rec.Code)
		}
		var audits int
		if err := owner.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND action = 'user.password_reset'
			AND subject_id = $2 AND actor_id = $3`, tenantID, u.id.String(), u.id).Scan(&audits); err != nil {
			t.Fatal(err)
		}
		if audits != 1 {
			t.Errorf("%d audit rows, want 1", audits)
		}
	})

	t.Run("a customer's token, an expired one and a short password are 422", func(t *testing.T) {
		s := seedAuthUser(ctx, t, store, uuid.Must(uuid.NewV7()))
		u := staffUser(t, s.email)
		for what, token := range map[string]string{
			"customer's": testResets(t).Mint(u.id, u.tenantID, u.hash, time.Now()),
			"expired":    u.reset(t, time.Now().Add(-auth.ResetTTL)),
			"forged":     "prt_x.y",
		} {
			t.Run(what, func(t *testing.T) {
				rec, p := post("/v1/auth/password-reset/confirm", map[string]any{"token": token, "password": "long-enough"})
				assertProblem(t, rec.Code, p, 422, "validation_failed", "token")
			})
		}
		rec, p := post("/v1/auth/password-reset/confirm", map[string]any{"token": u.reset(t, time.Now()), "password": "short"})
		assertProblem(t, rec.Code, p, 422, "validation_failed", "password")
		if rec, _ := post("/v1/auth/login", map[string]any{"email": s.email, "password": s.password}); rec.Code != 200 {
			t.Errorf("a refused reset changed the password: login %d", rec.Code)
		}
	})
}

// TestStaffPasswordResetLimit is BR-014's login row for staff reset: the
// eleventh request for one email in 15 minutes is 429, before any lookup.
func TestStaffPasswordResetLimit(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	signer, err := auth.NewSigner(isoSigningKey)
	if err != nil {
		t.Fatal(err)
	}
	limits := httpapi.NewRateLimiter(countingOnly{n: map[string]int{}}, httpapi.AdminRateLimit, httpapi.AdminRateWindow)
	h := httpapi.NewRouter(httpapi.NewServer(httpapi.Services{Team: team.NewService(store, testRedis(t), nil), Limits: limits}, false), signer, limits)
	address := "reset-limit-" + uuid.NewString() + "@example.com"
	ask := func() int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, jsonRequest(t, "/v1/auth/password-reset", map[string]any{"email": address}))
		return rec.Code
	}
	for i := 1; i <= 10; i++ {
		if code := ask(); code != 202 {
			t.Fatalf("try %d: %d", i, code)
		}
	}
	if code := ask(); code != http.StatusTooManyRequests {
		t.Errorf("try 11: %d, want 429", code)
	}
}
