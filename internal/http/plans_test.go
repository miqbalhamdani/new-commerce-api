package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// putOnPlan moves a seeded shop to plan. tenants has no RLS; the WHERE names
// the shop, as UpdateSettings names the caller's own.
func putOnPlan(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID, plan string) {
	t.Helper()
	tctx := tenant.NewContext(ctx, tenantID)
	if err := store.InTenantTx(tctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(tctx, `UPDATE tenants SET plan = $1 WHERE id = $2`, plan, tenantID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// TestPlans is P1-922's acceptance (BR-160): a shop's plan caps every role, in
// the session and in every check.
func TestPlans(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	srv := newServer(t)

	// ownerOn is a signed-in owner of a fresh shop on plan, and their session.
	ownerOn := func(t *testing.T, plan string) (s seeded, permissions []string) {
		t.Helper()
		id := uuid.Must(uuid.NewV7())
		s = seedSignedInUserWithRole(ctx, t, store, id, auth.RoleOwner)
		putOnPlan(ctx, t, store, id, plan)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, jsonRequest(t, "/v1/auth/login", map[string]string{"email": s.email, "password": s.password}))
		var session struct {
			AccessToken string `json:"access_token"`
			User        struct{ Permissions []string }
			Tenant      struct{ Plan string }
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &session) != nil || session.Tenant.Plan != plan {
			t.Fatalf("login on %s: %d %s", plan, rec.Code, rec.Body)
		}
		s.accessToken = session.AccessToken
		return s, session.User.Permissions
	}
	status := func(token, method, path string, body any) (int, string) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, platformRequest(t, method, path, token, body))
		return rec.Code, rec.Body.String()
	}

	t.Run("mulai: the catalog, settings and the activity log, nothing else", func(t *testing.T) {
		s, perms := ownerOn(t, auth.PlanMulai)
		if want := auth.PermissionsIn(auth.RoleOwner, auth.PlanMulai); !slices.Equal(perms, want) {
			t.Errorf("permissions\ngot:  %v\nwant: %v", perms, want)
		}
		for _, path := range []string{"/v1/products", "/v1/settings", "/v1/audit-log"} {
			if code, body := status(s.accessToken, http.MethodGet, path, nil); code != http.StatusOK {
				t.Errorf("GET %s: %d %s", path, code, body)
			}
		}
		for path, perm := range map[string]string{
			"/v1/orders": auth.PermOrdersRead, "/v1/customers": auth.PermCustomersRead, "/v1/blog/posts": auth.PermBlogRead,
			"/v1/users": auth.PermUsersRead, "/v1/api-keys": auth.PermAPIKeysRead,
		} {
			if code, body := status(s.accessToken, http.MethodGet, path, nil); code != http.StatusForbidden || !strings.Contains(body, perm) {
				t.Errorf("GET %s: %d %s, want 403 naming %s", path, code, body, perm)
			}
		}
		// A permission checked inside the handler is capped too.
		code, body := status(s.accessToken, http.MethodPost, "/v1/media/presign", map[string]any{"purpose": "blog_cover",
			"mime_type": "image/jpeg", "bytes": 100, "sha256": strings.Repeat("a", 64), "post_id": uuid.NewString()})
		if code != http.StatusForbidden || !strings.Contains(body, auth.PermBlogWrite) {
			t.Errorf("presign a blog cover: %d %s", code, body)
		}
	})

	t.Run("katalog: plus the blog, team and API keys", func(t *testing.T) {
		s, perms := ownerOn(t, auth.PlanKatalog)
		if slices.Contains(perms, auth.PermOrdersRead) || !slices.Contains(perms, auth.PermAPIKeysWrite) {
			t.Errorf("permissions %v", perms)
		}
		for _, path := range []string{"/v1/blog/posts", "/v1/users", "/v1/api-keys"} {
			if code, body := status(s.accessToken, http.MethodGet, path, nil); code != http.StatusOK {
				t.Errorf("GET %s: %d %s", path, code, body)
			}
		}
		if code, _ := status(s.accessToken, http.MethodGet, "/v1/orders", nil); code != http.StatusForbidden {
			t.Errorf("GET /v1/orders: %d", code)
		}
	})

	t.Run("jualan: the role's permissions, all of them", func(t *testing.T) {
		s, perms := ownerOn(t, auth.PlanJualan)
		if !slices.Equal(perms, auth.PermissionsFor(auth.RoleOwner)) {
			t.Errorf("permissions %v", perms)
		}
		if code, body := status(s.accessToken, http.MethodGet, "/v1/orders", nil); code != http.StatusOK {
			t.Errorf("GET /v1/orders: %d %s", code, body)
		}
	})

	t.Run("a token from before plans is read as jualan", func(t *testing.T) {
		id := uuid.Must(uuid.NewV7())
		s := seedSignedInUserWithRole(ctx, t, store, id, auth.RoleOwner)
		signer, err := auth.NewSigner(isoSigningKey)
		if err != nil {
			t.Fatal(err)
		}
		var userID uuid.UUID
		if err := store.InTenantTx(tenant.NewContext(ctx, id), func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, s.email).Scan(&userID)
		}); err != nil {
			t.Fatal(err)
		}
		old, err := signer.Issue(userID, id, auth.RoleOwner, "", "", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if code, body := status(old, http.MethodGet, "/v1/orders", nil); code != http.StatusOK {
			t.Errorf("GET /v1/orders: %d %s", code, body)
		}
	})
}

// TestAccountsPerPlan is P1-923's acceptance (BR-162): active and invited
// accounts, the owner included, stop at the plan's number.
func TestAccountsPerPlan(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	srv := newServer(t)
	call := func(token, method, path string, body any) (int, string) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, platformRequest(t, method, path, token, body))
		return rec.Code, rec.Body.String()
	}
	invite := func(token string) (code int, id string) {
		code, body := call(token, http.MethodPost, "/v1/users/invite", map[string]any{
			"email": "staf-" + uuid.NewString() + "@example.com", "name": "Staf", "role": "admin"})
		var u struct{ ID string }
		_ = json.Unmarshal([]byte(body), &u)
		return code, u.ID
	}

	t.Run("katalog: the owner and one more; a disabled user frees the seat but cannot come back", func(t *testing.T) {
		id := uuid.Must(uuid.NewV7())
		owner := seedSignedInUserWithRole(ctx, t, store, id, auth.RoleOwner)
		putOnPlan(ctx, t, store, id, auth.PlanKatalog)
		owner = signInAgain(ctx, t, store, owner)

		code, first := invite(owner.accessToken)
		if code != http.StatusCreated {
			t.Fatalf("first invite: %d", code)
		}
		if code, _ := invite(owner.accessToken); code != http.StatusUnprocessableEntity {
			t.Errorf("a third account: %d, want 422", code)
		}
		if code, body := call(owner.accessToken, http.MethodDelete, "/v1/users/"+first, nil); code != http.StatusNoContent {
			t.Fatalf("disable: %d %s", code, body)
		}
		if code, _ := invite(owner.accessToken); code != http.StatusCreated {
			t.Errorf("invite after a disable: %d", code)
		}
		if code, _ := call(owner.accessToken, http.MethodPatch, "/v1/users/"+first, map[string]any{"status": "active"}); code != http.StatusUnprocessableEntity {
			t.Errorf("re-enable past the limit: %d, want 422", code)
		}
	})

	t.Run("a downgrade signs no one out and only stops new accounts", func(t *testing.T) {
		id := uuid.Must(uuid.NewV7())
		owner := seedSignedInUserWithRole(ctx, t, store, id, auth.RoleOwner)
		admin := signInAnotherUser(ctx, t, store, id, auth.RoleAdmin)
		signInAnotherUser(ctx, t, store, id, auth.RoleOps)
		putOnPlan(ctx, t, store, id, auth.PlanKatalog)
		owner = signInAgain(ctx, t, store, owner)
		if code, _ := call(admin.accessToken, http.MethodGet, "/v1/products", nil); code != http.StatusOK {
			t.Errorf("the admin after the downgrade: %d", code)
		}
		if code, _ := invite(owner.accessToken); code != http.StatusUnprocessableEntity {
			t.Errorf("invite with 3 accounts on katalog: %d, want 422", code)
		}
	})
}

// signInAgain is a fresh session for a seeded user, after their shop changed.
func signInAgain(ctx context.Context, t *testing.T, store *db.Store, s seeded) seeded {
	t.Helper()
	signer, err := auth.NewSigner(isoSigningKey)
	if err != nil {
		t.Fatal(err)
	}
	session, err := auth.NewService(store, signer).Login(ctx, s.email, s.password)
	if err != nil {
		t.Fatal(err)
	}
	s.accessToken, s.refreshToken = session.AccessToken, session.RefreshToken
	return s
}
