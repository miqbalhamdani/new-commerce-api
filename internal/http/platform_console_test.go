package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
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

	t.Run("status: suspend signs the shop out and refuses sign-in; active restores it", func(t *testing.T) {
		shopID := homeOf(t, owner, other.email)
		login := func() int {
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, jsonRequest(t, "/v1/auth/login", map[string]string{"email": other.email, "password": other.password}))
			return rec.Code
		}
		path := "/v1/platform/shops/" + shopID.String()
		rec := call(http.MethodPatch, path, founder.accessToken, map[string]any{"status": "suspended"})
		var shop struct{ Status string }
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &shop) != nil || shop.Status != "suspended" {
			t.Fatalf("suspend: %d %s", rec.Code, rec.Body)
		}
		var live int
		if err := owner.QueryRow(ctx, `SELECT count(*) FROM refresh_tokens WHERE tenant_id = $1 AND revoked_at IS NULL`, shopID).Scan(&live); err != nil || live != 0 {
			t.Errorf("%d live refresh tokens after suspend (%v)", live, err)
		}
		if code := login(); code != http.StatusUnauthorized {
			t.Errorf("login while suspended: %d", code)
		}
		if n := audits(shopID, "tenant.status"); n != 1 {
			t.Errorf("%d tenant.status rows, want 1", n)
		}
		if rec := call(http.MethodPatch, path, founder.accessToken, map[string]any{"status": "active"}); rec.Code != http.StatusOK {
			t.Fatalf("reactivate: %d %s", rec.Code, rec.Body)
		}
		if code := login(); code != http.StatusOK {
			t.Errorf("login once active: %d", code)
		}
	})

	t.Run("status: an unknown status is 422, an unknown shop 404", func(t *testing.T) {
		path := "/v1/platform/shops/" + homeID.String()
		if rec := call(http.MethodPatch, path, founder.accessToken, map[string]any{"status": "deleted"}); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("unknown status: %d %s", rec.Code, rec.Body)
		}
		if rec := call(http.MethodPatch, "/v1/platform/shops/"+uuid.NewString(), founder.accessToken, map[string]any{"status": "active"}); rec.Code != http.StatusNotFound {
			t.Errorf("unknown shop: %d %s", rec.Code, rec.Body)
		}
	})

	t.Run("hand-over: change the invited owner, send the invite; 422 once confirmed", func(t *testing.T) {
		shop, _ := open(t, false)
		shopID := uuid.MustParse(shop.ID)
		next := "owner-" + uuid.NewString() + "@example.com"
		rec := call(http.MethodPatch, "/v1/platform/shops/"+shop.ID+"/owner", founder.accessToken, map[string]any{"email": next})
		var got platformShop
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Owner == nil || got.Owner.Email != next {
			t.Fatalf("change owner: %d %s", rec.Code, rec.Body)
		}
		if n := audits(shopID, "user.owner_change"); n != 1 {
			t.Errorf("%d user.owner_change rows, want 1", n)
		}
		if rec := call(http.MethodPatch, "/v1/platform/shops/"+shop.ID+"/owner", founder.accessToken, map[string]any{"email": other.email}); rec.Code != http.StatusUnprocessableEntity || !bytes.Contains(rec.Body.Bytes(), []byte(`"email"`)) {
			t.Errorf("a taken email: %d %s", rec.Code, rec.Body)
		}

		before := emails()
		if rec := call(http.MethodPost, "/v1/platform/shops/"+shop.ID+"/invite", founder.accessToken, nil); rec.Code != http.StatusNoContent {
			t.Fatalf("invite: %d %s", rec.Code, rec.Body)
		}
		if emails() != before+1 || audits(shopID, "user.resend_signup") != 1 {
			t.Error("the invite was not queued and audited")
		}

		if _, err := owner.Exec(ctx, `UPDATE users SET status = 'active', password_hash = 'x' WHERE email = $1`, next); err != nil {
			t.Fatal(err)
		}
		if rec := call(http.MethodPost, "/v1/platform/shops/"+shop.ID+"/invite", founder.accessToken, nil); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("invite a confirmed owner: %d", rec.Code)
		}
		if rec := call(http.MethodPatch, "/v1/platform/shops/"+shop.ID+"/owner", founder.accessToken, map[string]any{"name": "Lain"}); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("change a confirmed owner: %d", rec.Code)
		}
		if rec := call(http.MethodPost, "/v1/platform/shops/"+uuid.NewString()+"/invite", founder.accessToken, nil); rec.Code != http.StatusNotFound {
			t.Errorf("an unknown shop: %d", rec.Code)
		}
	})

	t.Run("impersonate: an owner session in that shop, audited, no cookie", func(t *testing.T) {
		shopID := homeOf(t, owner, other.email)
		rec := call(http.MethodPost, "/v1/platform/shops/"+shopID.String()+"/impersonate", founder.accessToken, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("impersonate: %d %s", rec.Code, rec.Body)
		}
		if c := rec.Header().Get("Set-Cookie"); c != "" {
			t.Errorf("an impersonation set a cookie: %s", c)
		}
		var session struct {
			AccessToken string `json:"access_token"`
			ExpiresIn   int    `json:"expires_in"`
			User        struct {
				ID            string   `json:"id"`
				Role          string   `json:"role"`
				Permissions   []string `json:"permissions"`
				PlatformAdmin bool     `json:"platform_admin"`
				Impersonating bool     `json:"impersonating"`
			} `json:"user"`
			Tenant struct{ ID string } `json:"tenant"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil {
			t.Fatal(err)
		}
		u := session.User
		if !u.Impersonating || u.PlatformAdmin || u.Role != "owner" || u.ID != founderID.String() ||
			session.Tenant.ID != shopID.String() || session.ExpiresIn != 3600 || slices.Contains(u.Permissions, auth.PermCustomersPurge) ||
			!slices.Contains(u.Permissions, auth.PermSettingsWrite) {
			t.Errorf("session %+v", session)
		}
		if n := audits(shopID, "platform.impersonate"); n != 1 {
			t.Errorf("%d platform.impersonate rows, want 1", n)
		}
		imp := session.AccessToken

		// It reads that shop, and only that shop.
		rec = call(http.MethodGet, "/v1/users", imp, nil)
		if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(other.email)) || bytes.Contains(rec.Body.Bytes(), []byte(founder.email)) {
			t.Errorf("GET /v1/users: %d %s", rec.Code, rec.Body)
		}
		// A write is the founder's, in that shop.
		rec = call(http.MethodPost, "/v1/users/invite", imp, map[string]any{"email": "staf-" + uuid.NewString() + "@example.com", "name": "Staf", "role": "admin"})
		if rec.Code != http.StatusCreated {
			t.Fatalf("invite as impersonator: %d %s", rec.Code, rec.Body)
		}
		if n := audits(shopID, "user.invite"); n != 1 {
			t.Errorf("%d user.invite rows by the founder, want 1", n)
		}
		// A post has no author: the founder is no user of the shop.
		if rec := call(http.MethodPost, "/v1/blog/posts", imp, map[string]any{"title": "Halo"}); rec.Code != http.StatusCreated ||
			!bytes.Contains(rec.Body.Bytes(), []byte(`"author":null`)) {
			t.Errorf("a post while impersonating: %d %s", rec.Code, rec.Body)
		}
		// Purging a shopper and the platform routes stay out of reach.
		if rec := call(http.MethodPost, "/v1/customers/"+uuid.NewString()+"/purge", imp, nil); rec.Code != http.StatusForbidden {
			t.Errorf("purge while impersonating: %d", rec.Code)
		}
		if rec := call(http.MethodGet, "/v1/platform/shops", imp, nil); rec.Code != http.StatusForbidden {
			t.Errorf("platform while impersonating: %d", rec.Code)
		}
		// It ends the moment the founder is no longer a platform admin.
		rec = httptest.NewRecorder()
		platformServer(t, store).ServeHTTP(rec, platformRequest(t, http.MethodGet, "/v1/users", imp, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("after removal from PLATFORM_ADMIN_EMAILS: %d", rec.Code)
		}
	})

	t.Run("impersonate: anyone else is 403, an unknown shop 404", func(t *testing.T) {
		if rec := call(http.MethodPost, "/v1/platform/shops/"+homeID.String()+"/impersonate", other.accessToken, nil); rec.Code != http.StatusForbidden {
			t.Errorf("not an admin: %d", rec.Code)
		}
		if rec := call(http.MethodPost, "/v1/platform/shops/"+uuid.NewString()+"/impersonate", founder.accessToken, nil); rec.Code != http.StatusNotFound {
			t.Errorf("unknown shop: %d", rec.Code)
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

// homeOf is the shop an email's user belongs to.
func homeOf(t *testing.T, owner *pgx.Conn, address string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := owner.QueryRow(t.Context(), `SELECT tenant_id FROM users WHERE email = $1`, address).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
