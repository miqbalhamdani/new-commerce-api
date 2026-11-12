package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/email"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
)

// The platform console's routes (Phase 11, BR-151, BR-152). The suite's
// server names no platform admin, so each is 403 for tenant A's owner and can
// carry nothing of B's. They audit in the shop they act on, not the caller's,
// which the audit harness cannot name: TestPlatformConsole checks those rows.
func init() {
	for _, rt := range []route{
		{http.MethodPost, "/v1/platform/shops"},
		{http.MethodPatch, "/v1/platform/shops/{id}"},
		{http.MethodPatch, "/v1/platform/shops/{id}/owner"},
		{http.MethodPost, "/v1/platform/shops/{id}/invite"},
		{http.MethodPost, "/v1/platform/shops/{id}/impersonate"},
	} {
		isolationCases = append(isolationCases, isolationCase{route: rt,
			seed: func(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID) seeded {
				s := seedSignedInUserWithRole(ctx, t, store, tenantID, auth.RoleOwner)
				s.marker = s.email
				s.id = tenantID.String()
				return s
			},
			request: func(t *testing.T, s seeded) *http.Request {
				// Aimed at B's shop.
				path := "/v1/platform/shops"
				if rt.pattern != path {
					path = "/v1/platform/shops/" + s.otherID + rt.pattern[len("/v1/platform/shops/{id}"):]
				}
				return platformRequest(t, rt.method, path, s.accessToken, map[string]any{})
			}})
		auditExempt[rt] = "audited in the shop it acts on, which this harness cannot name: TestPlatformConsole checks its row"
	}
}

func platformRequest(t *testing.T, method, path, token string, body any) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, &buf)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

// TestPlatformConsole is Phase 11's acceptance in the API (BR-151, BR-152).
func TestPlatformConsole(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	owner, err := pgx.Connect(ctx, config.DatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.WithoutCancel(ctx)) })
	redis := testRedis(t)
	emails := func() int64 { n, _ := redis.Len(ctx, email.Stream); return n }

	homeID := uuid.Must(uuid.NewV7())
	founder := seedSignedInUserWithRole(ctx, t, store, homeID, auth.RoleOwner)
	other := seedSignedInUserWithRole(ctx, t, store, uuid.Must(uuid.NewV7()), auth.RoleOwner)
	srv := platformServer(t, store, founder.email)
	call := func(method, path, token string, body any) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, platformRequest(t, method, path, token, body))
		return rec
	}
	var founderID uuid.UUID
	if err := owner.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, founder.email).Scan(&founderID); err != nil {
		t.Fatal(err)
	}
	audits := func(tenantID uuid.UUID, action string) (n int) {
		if err := owner.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND action = $2 AND actor_id = $3`,
			tenantID, action, founderID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// open opens a shop for a fresh address and returns it.
	open := func(t *testing.T, sendInvite bool) (shop platformShop, address string) {
		t.Helper()
		address = "klien-" + uuid.NewString() + "@example.com"
		rec := call(http.MethodPost, "/v1/platform/shops", founder.accessToken, map[string]any{
			"shop_name": "Toko Klien", "owner_name": "Rina", "owner_email": address, "send_invite": sendInvite})
		if rec.Code != http.StatusCreated {
			t.Fatalf("open: %d %s", rec.Code, rec.Body)
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &shop); err != nil {
			t.Fatal(err)
		}
		cleanupSignup(t, owner, uuid.MustParse(shop.ID))
		return shop, address
	}

	t.Run("open: a held shop, invited owner, no email, audited", func(t *testing.T) {
		before := emails()
		shop, address := open(t, false)
		if shop.Source != "platform" || shop.Owner == nil || shop.Owner.Email != address || shop.Owner.Status != "invited" {
			t.Errorf("shop %+v", shop)
		}
		if emails() != before {
			t.Error("a held shop queued an email")
		}
		if n := audits(uuid.MustParse(shop.ID), "tenant.register"); n != 1 {
			t.Errorf("%d tenant.register rows by the founder, want 1", n)
		}
	})

	t.Run("open with send_invite queues the signup email", func(t *testing.T) {
		before := emails()
		open(t, true)
		if emails() != before+1 {
			t.Error("no signup email was queued")
		}
	})

	t.Run("open: a taken email is 422 on owner_email", func(t *testing.T) {
		rec := call(http.MethodPost, "/v1/platform/shops", founder.accessToken, map[string]any{
			"shop_name": "Dua", "owner_name": "Rina", "owner_email": other.email, "send_invite": false})
		if rec.Code != http.StatusUnprocessableEntity || !bytes.Contains(rec.Body.Bytes(), []byte(`"owner_email"`)) {
			t.Errorf("%d %s", rec.Code, rec.Body)
		}
	})

	t.Run("open: anyone else is 403", func(t *testing.T) {
		rec := call(http.MethodPost, "/v1/platform/shops", other.accessToken, map[string]any{
			"shop_name": "Tiga", "owner_name": "Rina", "owner_email": "x-" + uuid.NewString() + "@example.com", "send_invite": false})
		if rec.Code != http.StatusForbidden {
			t.Errorf("%d %s", rec.Code, rec.Body)
		}
	})
}
