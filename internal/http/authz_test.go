package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
)

// TestPermissionDenied is P1-012's acceptance: a 403 names the required
// permission in detail.
//
// It runs against GET /v1/roles on the real server rather than a handler built
// for the test, because flows.md 6 states the criterion in those terms -- "an
// ops user gets 403 with the required permission named in detail on any
// user-management endpoint" -- and /roles is one.
func TestPermissionDenied(t *testing.T) {
	ctx := t.Context()

	store, err := db.New(ctx, config.AppDatabaseURL())
	if err != nil {
		t.Fatalf("connect as app_user: %v\n\nIs PostgreSQL running and migrated?\n"+
			"  brew services start postgresql@18\n  make db-create && make migrate", err)
	}
	t.Cleanup(store.Close)
	srv := newServer(t)

	t.Run("ops is refused and told which permission it needed", func(t *testing.T) {
		s := seedSignedInUserWithRole(ctx, t, store, uuid.Must(uuid.NewV7()), auth.RoleOps)

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, bearerRequest(t, http.MethodGet, "/v1/roles", s.accessToken))

		if rec.Code != http.StatusForbidden {
			t.Fatalf("GET /v1/roles as ops = %d, want %d\nbody: %s",
				rec.Code, http.StatusForbidden, rec.Body)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
			t.Errorf("Content-Type = %q, want application/problem+json", ct)
		}

		var problem struct {
			Type    string `json:"type"`
			Detail  string `json:"detail"`
			Status  int    `json:"status"`
			TraceID string `json:"trace_id"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&problem); err != nil {
			t.Fatalf("decode problem: %v", err)
		}

		// The acceptance, literally. Without the permission in the message a
		// user cannot tell "you cannot do this" from "you cannot do this yet,
		// ask your owner for users:read".
		if !strings.Contains(problem.Detail, auth.PermUsersRead) {
			t.Errorf("detail does not name the permission: %q", problem.Detail)
		}
		if !strings.HasSuffix(problem.Type, "permission_denied") {
			t.Errorf("type = %q, want it to end in permission_denied", problem.Type)
		}
		if problem.Status != http.StatusForbidden {
			t.Errorf("body says status %d, response says %d", problem.Status, rec.Code)
		}
		if problem.TraceID == "" {
			t.Error("no trace_id; every error carries one")
		}
	})

	// The other half: the check must actually let the right roles through, or
	// a test that only ever sees 403 would pass against a handler that always
	// refuses.
	t.Run("admin is allowed through", func(t *testing.T) {
		s := seedSignedInUserWithRole(ctx, t, store, uuid.Must(uuid.NewV7()), auth.RoleAdmin)

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, bearerRequest(t, http.MethodGet, "/v1/roles", s.accessToken))

		if rec.Code != http.StatusOK {
			t.Fatalf("GET /v1/roles as admin = %d, want 200\nbody: %s", rec.Code, rec.Body)
		}

		var roles []struct {
			Name        string   `json:"name"`
			Permissions []string `json:"permissions"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&roles); err != nil {
			t.Fatalf("decode roles: %v", err)
		}
		if len(roles) != 5 {
			t.Errorf("got %d roles, want the 5 seeded ones", len(roles))
		}
	})

	// A missing token is a different problem from an insufficient one, and
	// reporting 403 here would tell an anonymous caller that the endpoint
	// exists and what it needs.
	t.Run("no token is 401, not 403", func(t *testing.T) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/roles", nil))

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET /v1/roles with no token = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
	})

	t.Run("every refused role names the same permission", func(t *testing.T) {
		for _, role := range []string{auth.RoleOps, auth.RoleWarehouse, auth.RoleViewer} {
			s := seedSignedInUserWithRole(ctx, t, store, uuid.Must(uuid.NewV7()), role)

			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, bearerRequest(t, http.MethodGet, "/v1/roles", s.accessToken))

			if rec.Code != http.StatusForbidden {
				t.Errorf("%s: got %d, want 403", role, rec.Code)
				continue
			}
			if !strings.Contains(rec.Body.String(), auth.PermUsersRead) {
				t.Errorf("%s: detail does not name %s: %s", role, auth.PermUsersRead, rec.Body)
			}
		}
	})
}

// TestLoginCarriesPermissions checks the other half of "5 seeded roles": what a
// client is told it may do.
func TestLoginCarriesPermissions(t *testing.T) {
	ctx := t.Context()

	store, err := db.New(ctx, config.AppDatabaseURL())
	if err != nil {
		t.Fatalf("connect as app_user: %v", err)
	}
	t.Cleanup(store.Close)
	srv := newServer(t)

	for _, role := range []string{auth.RoleOwner, auth.RoleAdmin, auth.RoleOps, auth.RoleWarehouse, auth.RoleViewer} {
		t.Run(role, func(t *testing.T) {
			s := seedSignedInUserWithRole(ctx, t, store, uuid.Must(uuid.NewV7()), role)

			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, jsonRequest(t, "/v1/auth/login", map[string]string{
				"email": s.email, "password": s.password,
			}))
			if rec.Code != http.StatusOK {
				t.Fatalf("login = %d\nbody: %s", rec.Code, rec.Body)
			}

			var body struct {
				User struct {
					Role        string   `json:"role"`
					Permissions []string `json:"permissions"`
				} `json:"user"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode session: %v", err)
			}

			if body.User.Role != role {
				t.Errorf("role = %q, want %q", body.User.Role, role)
			}
			// The client hides actions it does not find here, so a login that
			// under-reports permissions is a feature that silently disappears.
			want := auth.PermissionsFor(role)
			if len(body.User.Permissions) != len(want) {
				t.Errorf("got %d permissions, want %d\ngot:  %v\nwant: %v",
					len(body.User.Permissions), len(want), body.User.Permissions, want)
			}
		})
	}
}

func bearerRequest(t *testing.T, method, path, token string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

// seedSignedInUserWithRole is seedSignedInUser for a role other than the
// default, which the permission tests need on both sides of every check.
func seedSignedInUserWithRole(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID, role string) seeded {
	t.Helper()

	s := seedAuthUser(ctx, t, store, tenantID)
	setSeededRole(ctx, t, store, tenantID, s.email, role)

	signer, err := auth.NewSigner(isoSigningKey)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	session, err := auth.NewService(store, signer).Login(ctx, s.email, s.password)
	if err != nil {
		t.Fatalf("sign in %s: %v", role, err)
	}
	s.accessToken = session.AccessToken
	s.refreshToken = session.RefreshToken
	return s
}
