package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// Isolation cases for the auth routes (P1-011).
//
// Every one of these seeds a real user at tenant A and another at tenant B,
// then acts as A. What must never come back is B's tenant id, which appears in
// the Session body of a route that got the wrong tenant.
//
// Login and refresh are unauthenticated, which does not exempt them: they are
// the two routes that resolve a tenant from something other than an access
// token, so they are the two most able to resolve the wrong one.
func init() {
	isolationCases = append(isolationCases,
		isolationCase{
			route: route{method: "POST", pattern: "/v1/auth/login"},
			seed:  seedAuthUser,
			request: func(t *testing.T, s seeded) *http.Request {
				return jsonRequest(t, "/v1/auth/login", map[string]string{
					"email": s.email, "password": s.password,
				})
			},
		},
		isolationCase{
			route: route{method: "POST", pattern: "/v1/auth/refresh"},
			seed:  seedSignedInUser,
			request: func(t *testing.T, s seeded) *http.Request {
				r := jsonRequest(t, "/v1/auth/refresh", nil)
				r.AddCookie(&http.Cookie{Name: "refresh_token", Value: s.refreshToken})
				return r
			},
		},
		isolationCase{
			route: route{method: "GET", pattern: "/v1/roles"},
			// An admin, because ops would correctly get a 403 and the case
			// would then be asserting nothing about isolation.
			seed: func(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID) seeded {
				return seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleAdmin)
			},
			request: func(t *testing.T, s seeded) *http.Request {
				return bearerRequest(t, http.MethodGet, "/v1/roles", s.accessToken)
			},
		},
		isolationCase{
			route: route{method: "POST", pattern: "/v1/auth/logout"},
			seed:  seedSignedInUser,
			request: func(t *testing.T, s seeded) *http.Request {
				r := jsonRequest(t, "/v1/auth/logout", nil)
				r.Header.Set("Authorization", "Bearer "+s.accessToken)
				r.AddCookie(&http.Cookie{Name: "refresh_token", Value: s.refreshToken})
				return r
			},
		},
	)
}

// seedAuthUser creates a tenant and an active user in it.
func seedAuthUser(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID) seeded {
	t.Helper()

	userID := uuid.Must(uuid.NewV7())
	email := fmt.Sprintf("iso-%s@example.com", userID)

	hash, err := auth.HashPassword(isoPassword)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}

	tenantCtx := tenant.NewContext(ctx, tenantID)
	if err := store.InTenantTx(tenantCtx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO tenants (id, name, slug) VALUES ($1, $2, $3)`,
			tenantID, "Isolation "+tenantID.String(), "iso-"+tenantID.String()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO users (id, tenant_id, email, password_hash, name, role, status)
			 VALUES ($1, $2, $3, $4, 'Isolation user', 'ops', 'active')`,
			userID, tenantID, email, hash)
		return err
	}); err != nil {
		t.Fatalf("seed tenant %s: %v", tenantID, err)
	}

	t.Cleanup(func() {
		cleanup := context.WithoutCancel(ctx)
		_ = store.InTenantTx(tenant.NewContext(cleanup, tenantID), func(tx pgx.Tx) error {
			if _, err := tx.Exec(cleanup, `DELETE FROM users WHERE id = $1`, userID); err != nil {
				return err
			}
			_, err := tx.Exec(cleanup, `DELETE FROM tenants WHERE id = $1`, tenantID)
			return err
		})
	})

	// The tenant id is the marker: it is what a Session body carries, so it is
	// what would appear if a route resolved the wrong tenant.
	return seeded{marker: tenantID.String(), email: email, password: isoPassword}
}

// seedSignedInUser is seedAuthUser plus a real login, for routes that need a
// live session to reach at all.
func seedSignedInUser(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID) seeded {
	t.Helper()

	s := seedAuthUser(ctx, t, store, tenantID)

	signer, err := auth.NewSigner(isoSigningKey)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	session, err := auth.NewService(store, signer).Login(ctx, s.email, s.password)
	if err != nil {
		t.Fatalf("sign in seeded user: %v", err)
	}

	s.accessToken = session.AccessToken
	s.refreshToken = session.RefreshToken
	return s
}

func jsonRequest(t *testing.T, path string, body any) *http.Request {
	t.Helper()

	var payload string
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode body: %v", err)
		}
		payload = string(encoded)
	}
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload))
	r.Header.Set("Content-Type", "application/json")
	return r
}

// setSeededRole changes a seeded user's role. Seeding creates an ops user; the
// permission tests need one on each side of every check.
func setSeededRole(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID, email, role string) {
	t.Helper()
	if err := store.InTenantTx(tenant.NewContext(ctx, tenantID), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE users SET role = $2 WHERE email = $1`, email, role)
		return err
	}); err != nil {
		t.Fatalf("set role %s: %v", role, err)
	}
}
