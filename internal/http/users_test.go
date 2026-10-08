package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/email"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

func init() {
	byID := func(method, suffix string, body any) func(t *testing.T, s seeded) *http.Request {
		return func(t *testing.T, s seeded) *http.Request {
			if body == nil {
				return bearerRequest(t, method, "/v1/users/"+s.otherID+suffix, s.accessToken)
			}
			return bodyRequest(t, method, "/v1/users/"+s.otherID+suffix, s.accessToken, body)
		}
	}
	isolationCases = append(isolationCases,
		isolationCase{route: route{"GET", "/v1/users"}, seed: seedInvited,
			request: func(t *testing.T, s seeded) *http.Request {
				return bearerRequest(t, http.MethodGet, "/v1/users?limit=200", s.accessToken)
			}},
		isolationCase{route: route{"POST", "/v1/users/invite"}, seed: seedInvited,
			request: func(t *testing.T, s seeded) *http.Request {
				return bodyRequest(t, http.MethodPost, "/v1/users/invite", s.accessToken,
					map[string]any{"email": "new-" + uuid.NewString() + "@example.com", "name": "New", "role": "ops"})
			}},
		isolationCase{route: route{"POST", "/v1/users/{id}/resend-invite"}, seed: seedInvited, request: byID(http.MethodPost, "/resend-invite", nil)},
		isolationCase{route: route{"PATCH", "/v1/users/{id}"}, seed: seedInvited, request: byID(http.MethodPatch, "", map[string]any{"role": "viewer"})},
		isolationCase{route: route{"DELETE", "/v1/users/{id}"}, seed: seedInvited, request: byID(http.MethodDelete, "", nil)},
		isolationCase{route: route{"POST", "/v1/auth/accept-invite"}, seed: seedInvited,
			request: func(t *testing.T, s seeded) *http.Request {
				return jsonRequest(t, "/v1/auth/accept-invite", map[string]any{"token": s.key, "password": "long-enough"})
			}},
	)
	auditCases = append(auditCases,
		auditCase{route: route{"POST", "/v1/users/invite"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			return bodyRequest(t, http.MethodPost, "/v1/users/invite", s.accessToken,
				map[string]any{"email": "audit-" + uuid.NewString() + "@example.com", "name": "A", "role": "ops"}), "user", ""
		}},
		auditCase{route: route{"POST", "/v1/users/{id}/resend-invite"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			id := invite(t, s, "viewer")
			return bearerRequest(t, http.MethodPost, "/v1/users/"+id+"/resend-invite", s.accessToken), "user", id
		}},
		auditCase{route: route{"PATCH", "/v1/users/{id}"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			id := invite(t, s, "viewer")
			return bodyRequest(t, http.MethodPatch, "/v1/users/"+id, s.accessToken, map[string]any{"role": "ops"}), "user", id
		}},
		auditCase{route: route{"DELETE", "/v1/users/{id}"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			id := invite(t, s, "viewer")
			acceptAs(t, s, id) // only an active user can be disabled into a meaningful state
			return bearerRequest(t, http.MethodDelete, "/v1/users/"+id, s.accessToken), "user", id
		}},
		auditCase{route: route{"POST", "/v1/auth/accept-invite"}, request: func(t *testing.T, s seeded) (*http.Request, string, string) {
			id := invite(t, s, "viewer")
			return jsonRequest(t, "/v1/auth/accept-invite", map[string]any{"token": tokenFor(t, s, id), "password": "long-enough"}), "user", id
		}},
	)
}

// seedInvited is a signed-in owner plus an invited user whose email is the
// marker; key is that user's invitation token.
func seedInvited(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID) seeded {
	t.Helper()
	s := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleOwner)
	id := uuid.Must(uuid.NewV7())
	s.id = id.String()
	s.marker = "invited-" + id.String() + "@example.com"
	if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO users (id, tenant_id, email, name, role) VALUES ($1, $2, $3, 'Invited', 'ops')`,
			id, tenantID, s.marker)
		return err
	}); err != nil {
		t.Fatalf("seed invited: %v", err)
	}
	t.Cleanup(func() {
		bg := tenant.NewContext(context.WithoutCancel(ctx), tenantID)
		_ = store.InTenantTx(bg, func(tx pgx.Tx) error {
			_, err := tx.Exec(bg, `DELETE FROM refresh_tokens WHERE user_id = $1; DELETE FROM users WHERE id = $1`, id)
			return err
		})
	})
	s.key = testInvites(t).Mint(id, tenantID, time.Now())
	return s
}

// TestUsers is P1-064's acceptance (04-api-spec.md §2, §4; BR-020, BR-023,
// BR-026, BR-027).
func TestUsers(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	tenantID := uuid.Must(uuid.NewV7())
	owner := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleOwner)
	do := apiClient(t, owner)
	redis := testRedis(t)
	emails := func() int64 { n, _ := redis.Len(ctx, email.Stream); return n }

	before := emails()
	code, u := do("POST", "/v1/users/invite", map[string]any{"email": "rina-" + uuid.NewString() + "@example.com", "name": "Rina", "role": "ops"})
	if code != 201 || u["status"] != "invited" || u["last_login_at"] != nil {
		t.Fatalf("invite: %d %v", code, u)
	}
	if emails() != before+1 {
		t.Error("the invitation was not queued after commit")
	}
	rina := u["id"].(string)

	t.Run("an email in use anywhere is 422, and a failed invite queues nothing", func(t *testing.T) {
		before := emails()
		code, p := do("POST", "/v1/users/invite", map[string]any{"email": owner.email, "name": "Dup", "role": "ops"})
		assertProblem(t, code, p, 422, "validation_failed", "email")
		if emails() != before {
			t.Error("a rolled-back invite queued an email")
		}
	})
	t.Run("only an owner grants owner", func(t *testing.T) {
		admin := signInAnotherUser(ctx, t, store, tenantID, auth.RoleAdmin)
		code, p := apiClient(t, admin)("POST", "/v1/users/invite",
			map[string]any{"email": "o-" + uuid.NewString() + "@example.com", "name": "O", "role": "owner"})
		assertProblem(t, code, p, 403, "permission_denied", "")
		code, p = apiClient(t, admin)("PATCH", "/v1/users/"+rina, map[string]any{"role": "owner"})
		assertProblem(t, code, p, 403, "permission_denied", "")
	})
	t.Run("accepting signs in once; the link then stops working", func(t *testing.T) {
		token := tokenFor(t, owner, rina)
		rec := httptest.NewRecorder()
		newServer(t).ServeHTTP(rec, jsonRequest(t, "/v1/auth/accept-invite", map[string]any{"token": token, "password": "rina-password"}))
		if rec.Code != 200 || rec.Result().Cookies()[0].Name != "refresh_token" {
			t.Fatalf("accept: %d %s", rec.Code, rec.Body)
		}
		rec = httptest.NewRecorder()
		newServer(t).ServeHTTP(rec, jsonRequest(t, "/v1/auth/accept-invite", map[string]any{"token": token, "password": "rina-password"}))
		if rec.Code != 422 {
			t.Errorf("second accept: %d %s", rec.Code, rec.Body)
		}
		expired := testInvites(t).Mint(uuid.MustParse(rina), tenantID, time.Now().Add(-8*24*time.Hour))
		rec = httptest.NewRecorder()
		newServer(t).ServeHTTP(rec, jsonRequest(t, "/v1/auth/accept-invite", map[string]any{"token": expired, "password": "rina-password"}))
		if rec.Code != 422 {
			t.Errorf("expired token: %d", rec.Code)
		}
		code, p := do("POST", "/v1/users/"+rina+"/resend-invite", nil)
		assertProblem(t, code, p, 422, "validation_failed", "status")
	})
	t.Run("the last active owner cannot be demoted or disabled", func(t *testing.T) {
		var ownerID string
		_, list := do("GET", "/v1/users?role=owner", nil)
		ownerID = list["data"].([]any)[0].(map[string]any)["id"].(string)
		code, p := do("PATCH", "/v1/users/"+ownerID, map[string]any{"role": "admin"})
		assertProblem(t, code, p, 422, "validation_failed", "role")
		code, p = do("DELETE", "/v1/users/"+ownerID, nil)
		assertProblem(t, code, p, 422, "validation_failed", "role")
	})
	t.Run("an invited user is activated only by accepting", func(t *testing.T) {
		id := invite(t, owner, "viewer")
		code, p := do("PATCH", "/v1/users/"+id, map[string]any{"status": "active"})
		assertProblem(t, code, p, 422, "validation_failed", "status")
	})
	t.Run("disabling revokes refresh tokens", func(t *testing.T) {
		victim := signInAnotherUser(ctx, t, store, tenantID, auth.RoleViewer)
		_, list := do("GET", "/v1/users?role=viewer&status=active", nil)
		var id string
		for _, u := range list["data"].([]any) {
			if u.(map[string]any)["email"] == victim.email {
				id = u.(map[string]any)["id"].(string)
			}
		}
		if code, _ := do("DELETE", "/v1/users/"+id, nil); code != 204 {
			t.Fatalf("disable: %d", code)
		}
		r := jsonRequest(t, "/v1/auth/refresh", nil)
		r.AddCookie(&http.Cookie{Name: "refresh_token", Value: victim.refreshToken})
		rec := httptest.NewRecorder()
		newServer(t).ServeHTTP(rec, r)
		if rec.Code != 401 {
			t.Errorf("refresh after disable: %d", rec.Code)
		}
		code, u := do("PATCH", "/v1/users/"+id, map[string]any{"status": "active"})
		if code != 200 || u["status"] != "active" {
			t.Errorf("re-enable: %d %v", code, u)
		}
	})
	t.Run("ops sees no users", func(t *testing.T) {
		ops := signInAnotherUser(ctx, t, store, tenantID, auth.RoleOps)
		code, p := apiClient(t, ops)("GET", "/v1/users", nil)
		assertProblem(t, code, p, 403, "permission_denied", "")
	})
}

// invite makes an invited user through the API and returns its id.
func invite(t *testing.T, s seeded, role string) string {
	t.Helper()
	return apiCreate(t, s, "/v1/users/invite", map[string]any{"email": "inv-" + uuid.NewString() + "@example.com",
		"name": "Invited", "role": role})
}

// tokenFor mints the invitation token the worker would have emailed. The
// API has no route that returns a user's tenant, so it is read as the
// database owner.
func tokenFor(t *testing.T, _ seeded, userID string) string {
	t.Helper()
	owner, err := pgx.Connect(t.Context(), config.DatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close(t.Context()) }()
	var tenantID uuid.UUID
	if err := owner.QueryRow(t.Context(), `SELECT tenant_id FROM users WHERE id = $1`, userID).Scan(&tenantID); err != nil {
		t.Fatalf("tenant of user: %v", err)
	}
	return testInvites(t).Mint(uuid.MustParse(userID), tenantID, time.Now())
}

func acceptAs(t *testing.T, _ seeded, userID string) {
	t.Helper()
	rec := httptest.NewRecorder()
	newServer(t).ServeHTTP(rec, jsonRequest(t, "/v1/auth/accept-invite",
		map[string]any{"token": tokenFor(t, seeded{}, userID), "password": "long-enough"}))
	if rec.Code != 200 {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body)
	}
}
