package team_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/email"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/queue"
	"github.com/miqbalhamdani/new-commerce-api/internal/team"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

type recorder struct {
	mu   sync.Mutex
	sent []email.Message
}

func (r *recorder) Send(_ context.Context, m email.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, m)
	return nil
}

func (r *recorder) all() []email.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]email.Message(nil), r.sent...)
}

// TestInvitationEmail is P1-226's acceptance at the worker: an enqueued
// invitation goes out once, from the shop's name, with Reply-To, carrying a
// link whose token the API will accept (BR-026, BR-128).
func TestInvitationEmail(t *testing.T) {
	ctx := t.Context()
	store, err := db.New(ctx, config.AppDatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	redis, err := queue.New(ctx, config.RedisURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = redis.Close() })
	invites, _ := auth.NewInviteSigner(strings.Repeat("k", 32))

	tenantID, owner, invited := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	tctx := tenant.NewContext(ctx, tenantID)
	if err := store.InTenantTx(tctx, func(tx pgx.Tx) error {
		for _, sql := range []string{
			`INSERT INTO tenants (id, name, slug, order_prefix) VALUES ('` + tenantID.String() + `', 'Toko ABC', 'toko-` + tenantID.String() + `', 'TKA')`,
			`INSERT INTO users (id, tenant_id, email, name, role, status) VALUES ('` + owner.String() + `', '` + tenantID.String() + `', 'owner-` + owner.String() + `@tokoabc.com', 'Budi', 'owner', 'active')`,
			`INSERT INTO users (id, tenant_id, email, name, role, status) VALUES ('` + invited.String() + `', '` + tenantID.String() + `', 'rina-` + invited.String() + `@example.com', 'Rina', 'ops', 'invited')`,
		} {
			if _, err := tx.Exec(tctx, sql); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() {
		bg := tenant.NewContext(context.WithoutCancel(ctx), tenantID)
		_ = store.InTenantTx(bg, func(tx pgx.Tx) error {
			_, err := tx.Exec(bg, `DELETE FROM users; DELETE FROM tenants WHERE id = $1`, tenantID)
			return err
		})
	})

	rec := &recorder{}
	renders := map[string]email.Render{"invitation": team.InvitationMail(store, invites, "http://admin.test")}
	cctx, stop := context.WithCancel(ctx)
	defer stop()
	go func() { _ = email.Consume(cctx, redis, "test-"+tenantID.String(), renders, rec, time.Second, 3) }()

	if err := email.Enqueue(ctx, redis, email.Envelope{Kind: "invitation", TenantID: tenantID, UserID: invited, ActorID: owner}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var mine []email.Message
	for len(mine) == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		for _, m := range rec.all() {
			if strings.Contains(m.To, invited.String()) {
				mine = append(mine, m)
			}
		}
	}
	if len(mine) != 1 {
		t.Fatalf("%d emails to the invited user, want 1", len(mine))
	}
	m := mine[0]
	if m.FromName != "Toko ABC" || !strings.HasPrefix(m.ReplyTo, "owner-") || m.Subject != "Budi invited you to Toko ABC" {
		t.Errorf("message %+v", m)
	}
	_, token, _ := strings.Cut(m.Text, "#token=")
	token, _, _ = strings.Cut(token, "\n")
	inv, err := invites.Parse(token, time.Now())
	if err != nil || inv.UserID != invited || inv.TenantID != tenantID {
		t.Errorf("token in the link: %+v %v", inv, err)
	}
}

// TestInvitationReplyTo renders an invitation without the stream: Reply-To is
// the shop's contact email once it has one, and the inviter before (BR-128).
func TestInvitationReplyTo(t *testing.T) {
	ctx := t.Context()
	store, err := db.New(ctx, config.AppDatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	invites, _ := auth.NewInviteSigner(strings.Repeat("k", 32))
	tenantID, owner, invited := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	tctx := tenant.NewContext(ctx, tenantID)
	exec := func(sql string) {
		t.Helper()
		if err := store.InTenantTx(tctx, func(tx pgx.Tx) error { _, err := tx.Exec(tctx, sql); return err }); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}
	exec(`INSERT INTO tenants (id, name, slug, order_prefix) VALUES ('` + tenantID.String() + `', 'Toko ABC', 'toko-` + tenantID.String() + `', 'TKA')`)
	exec(`INSERT INTO users (id, tenant_id, email, name, role, status) VALUES ('` + owner.String() + `', current_setting('app.tenant_id')::uuid, 'budi-` + owner.String() + `@tokoabc.com', 'Budi', 'owner', 'active')`)
	exec(`INSERT INTO users (id, tenant_id, email, name, role, status) VALUES ('` + invited.String() + `', current_setting('app.tenant_id')::uuid, 'rina-` + invited.String() + `@example.com', 'Rina', 'ops', 'invited')`)
	t.Cleanup(func() {
		bg := tenant.NewContext(context.WithoutCancel(ctx), tenantID)
		_ = store.InTenantTx(bg, func(tx pgx.Tx) error {
			_, err := tx.Exec(bg, `DELETE FROM storefront_settings; DELETE FROM users; DELETE FROM tenants WHERE id = $1`, tenantID)
			return err
		})
	})
	render := team.InvitationMail(store, invites, "http://admin.test")
	env := email.Envelope{Kind: "invitation", TenantID: tenantID, UserID: invited, ActorID: owner}

	m, err := render(ctx, env)
	if err != nil || m == nil || !strings.HasPrefix(m.ReplyTo, "budi-") {
		t.Fatalf("no settings row: %+v %v, want Reply-To the inviter", m, err)
	}
	exec(`INSERT INTO storefront_settings (tenant_id, midtrans_webhook_id, contact_email)
		VALUES (current_setting('app.tenant_id')::uuid, gen_random_uuid(), 'halo@tokoabc.com')`)
	if m, err = render(ctx, env); err != nil || m.ReplyTo != "halo@tokoabc.com" || m.FromName != "Toko ABC" {
		t.Errorf("with a contact email: %+v %v", m, err)
	}
}

// TestSignupEmail is P1-084 at the worker: a signup's confirmation comes from
// Belli.id with no Reply-To, and its link is an invitation the API accepts,
// marked signup=1; once the owner is active nothing is sent (BR-026, BR-128).
func TestSignupEmail(t *testing.T) {
	ctx := t.Context()
	store, err := db.New(ctx, config.AppDatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	invites, _ := auth.NewInviteSigner(strings.Repeat("k", 32))

	tenantID, owner := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	tctx := tenant.NewContext(ctx, tenantID)
	exec := func(sql string, args ...any) {
		t.Helper()
		if err := store.InTenantTx(tctx, func(tx pgx.Tx) error { _, err := tx.Exec(tctx, sql, args...); return err }); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO tenants (id, name, slug, order_prefix) VALUES ($1, 'Erigo', $2, 'ERI')`, tenantID, "erigo-"+tenantID.String())
	exec(`INSERT INTO users (id, tenant_id, email, name, role, status) VALUES ($1, $2, $3, 'Budi', 'owner', 'invited')`,
		owner, tenantID, "budi-"+owner.String()+"@erigo.co.id")
	t.Cleanup(func() {
		bg := tenant.NewContext(context.WithoutCancel(ctx), tenantID)
		_ = store.InTenantTx(bg, func(tx pgx.Tx) error {
			_, err := tx.Exec(bg, `DELETE FROM users; DELETE FROM tenants WHERE id = $1`, tenantID)
			return err
		})
	})

	render := team.SignupMail(store, invites, "https://admin.example.com")
	m, err := render(ctx, email.Envelope{Kind: "signup", TenantID: tenantID, UserID: owner})
	if err != nil || m == nil {
		t.Fatalf("render: %v %v", m, err)
	}
	if m.FromName != "Belli.id" || m.ReplyTo != "" || m.To != "budi-"+owner.String()+"@erigo.co.id" || !strings.Contains(m.Text, "Erigo") {
		t.Errorf("message: %+v", m)
	}
	// P1-086: the subject leads with the 6-digit code, which the body repeats
	// and the API accepts for this owner.
	code, _, _ := strings.Cut(m.Subject, " ")
	if len(code) != 6 || !strings.HasSuffix(m.Subject, " is your Belli.id code") || !strings.Contains(m.Text, code) ||
		!invites.CheckCode(owner, tenantID, code, time.Now()) {
		t.Errorf("code %q in %q is not this owner's", code, m.Subject)
	}
	_, after, found := strings.Cut(m.Text, "https://admin.example.com/accept-invite#token=")
	token, rest, marked := strings.Cut(strings.Fields(after + " ")[0], "&")
	if !found || !marked || rest != "signup=1" {
		t.Fatalf("link not found in %q", m.Text)
	}
	if inv, err := invites.Parse(token, time.Now()); err != nil || inv.UserID != owner || inv.TenantID != tenantID {
		t.Errorf("token: %+v %v", inv, err)
	}

	exec(`UPDATE users SET status = 'active' WHERE id = $1`, owner)
	if m, err := render(ctx, email.Envelope{Kind: "signup", TenantID: tenantID, UserID: owner}); m != nil || err != nil {
		t.Errorf("an active owner got a signup email: %v %v", m, err)
	}
}
