package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	httpapi "github.com/miqbalhamdani/new-commerce-api/internal/http"
	"github.com/miqbalhamdani/new-commerce-api/internal/platformadmin"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

func init() {
	// P1-902: the suite's server names no platform admin, so tenant A's owner
	// gets 403 and tenant B's owner email (the marker) cannot come back.
	isolationCases = append(isolationCases,
		isolationCase{route: route{"GET", "/v1/platform/shops"},
			seed: func(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID) seeded {
				s := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleOwner)
				s.marker = s.email
				return s
			},
			request: func(t *testing.T, s seeded) *http.Request {
				return bearerRequest(t, http.MethodGet, "/v1/platform/shops", s.accessToken)
			}})
}

// platformServer is the admin API with emails as PLATFORM_ADMIN_EMAILS.
func platformServer(t *testing.T, store *db.Store, emails ...string) http.Handler {
	t.Helper()
	signer, err := auth.NewSigner(isoSigningKey)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	admins := auth.NewPlatformAdmins(emails)
	return httpapi.NewRouter(httpapi.NewServer(httpapi.Services{
		Auth:     auth.NewService(store, signer).WithPlatformAdmins(admins),
		Platform: platformadmin.NewService(store, admins),
	}, false), signer, httpapi.NewRateLimiter(testRedis(t), httpapi.AdminRateLimit, httpapi.AdminRateWindow))
}

type platformShop struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Owner  *struct {
		Email, Status string
	} `json:"owner"`
	Products struct {
		Active, Total int
	} `json:"products"`
	LastActivityAt *string `json:"last_activity_at"`
}

// TestPlatformShops is P1-902's acceptance (BR-150).
func TestPlatformShops(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)

	aID, bID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	a := seedSignedInUserWithRole(ctx, t, store, aID, auth.RoleOwner)
	b := seedSignedInUserWithRole(ctx, t, store, bID, auth.RoleOwner)
	// B signed up with Google and has a product; A was made by the CLI.
	if err := store.InTenantTx(tenant.NewContext(ctx, bID), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO audit_log (tenant_id, action, subject_type, subject_id, after)
			VALUES ($1, 'tenant.register', 'tenant', $2, '{"via": "google"}')`, bID, bID.String())
		return err
	}); err != nil {
		t.Fatalf("seed register row: %v", err)
	}
	dashProduct(ctx, t, store, bID, "Platform Tee "+bID.String())

	get := func(srv http.Handler, token string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, bearerRequest(t, http.MethodGet, "/v1/platform/shops", token))
		return rec
	}

	t.Run("a platform admin sees every shop", func(t *testing.T) {
		rec := get(platformServer(t, store, strings.ToUpper(a.email)), a.accessToken)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		if z := utcTimestamp.FindString(rec.Body.String()); z != "" {
			t.Errorf("UTC timestamp %s; the wire is +07:00 (BR-007)", z)
		}
		var body struct {
			Summary map[string]int `json:"summary"`
			Shops   []platformShop `json:"shops"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		for _, k := range []string{"shops", "new_7d", "new_30d", "verified", "website_set_up", "with_products",
			"with_orders", "active_7d", "sleeping"} {
			if _, ok := body.Summary[k]; !ok {
				t.Errorf("summary has no %s", k)
			}
		}
		shops := map[string]platformShop{}
		for _, s := range body.Shops {
			shops[s.ID] = s
		}
		gotA, okA := shops[aID.String()]
		gotB, okB := shops[bID.String()]
		if !okA || !okB {
			t.Fatalf("report is missing a shop: A %v, B %v", okA, okB)
		}
		if gotA.Source != "cli" || gotB.Source != "google" {
			t.Errorf("sources A %q, B %q; want cli, google", gotA.Source, gotB.Source)
		}
		if gotB.Owner == nil || gotB.Owner.Email != b.email || gotB.Owner.Status != "active" {
			t.Errorf("B's owner %+v, want %s, active", gotB.Owner, b.email)
		}
		if gotB.Products.Total != 1 || gotA.Products.Total != 0 {
			t.Errorf("products: A %d, B %d; want 0, 1", gotA.Products.Total, gotB.Products.Total)
		}
		if gotB.LastActivityAt == nil {
			t.Error("B signed in, so it has a last activity")
		}
	})

	t.Run("anyone else gets 403", func(t *testing.T) {
		rec := get(platformServer(t, store, a.email), b.accessToken)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "PLATFORM_ADMIN_EMAILS") {
			t.Errorf("status %d, body %s; want 403 naming PLATFORM_ADMIN_EMAILS", rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), a.email) {
			t.Error("a 403 carries a shop's data")
		}
	})

	t.Run("an empty setting lets no one in", func(t *testing.T) {
		if rec := get(platformServer(t, store), a.accessToken); rec.Code != http.StatusForbidden {
			t.Errorf("status %d, want 403", rec.Code)
		}
	})

	t.Run("the session says platform_admin", func(t *testing.T) {
		srv := platformServer(t, store, a.email)
		for _, c := range []struct {
			s    seeded
			want bool
		}{{a, true}, {b, false}} {
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, jsonRequest(t, "/v1/auth/login", map[string]string{"email": c.s.email, "password": c.s.password}))
			var session struct {
				User struct {
					PlatformAdmin *bool `json:"platform_admin"`
				} `json:"user"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil || rec.Code != http.StatusOK {
				t.Fatalf("login %s: %d %s", c.s.email, rec.Code, rec.Body)
			}
			if session.User.PlatformAdmin == nil || *session.User.PlatformAdmin != c.want {
				t.Errorf("%s: platform_admin %v, want %v", c.s.email, session.User.PlatformAdmin, c.want)
			}
		}
	})
}
