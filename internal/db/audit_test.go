package db

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// TestAudit is the recorder half of P1-018's acceptance: one row when the
// write commits, none when it rolls back (BR-018).
func TestAudit(t *testing.T) {
	ctx := t.Context()
	store, err := New(ctx, config.AppDatabaseURL())
	if err != nil {
		t.Fatalf("connect as app_user: %v", err)
	}
	t.Cleanup(store.Close)

	tenantID, userID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	ctx = tenant.NewContext(ctx, tenantID)
	ctx = tenant.NewActorContext(ctx, tenant.Actor{UserID: userID, IP: netip.MustParseAddr("203.0.113.7")})

	t.Cleanup(func() {
		_ = store.InTenantTx(context.WithoutCancel(ctx), func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `DELETE FROM audit_log`)
			return err
		})
	})

	count := func(subjectID string) int {
		t.Helper()
		var n int
		if err := store.InTenantTx(ctx, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE subject_id = $1`, subjectID).Scan(&n)
		}); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}

	t.Run("commit writes one row with actor, ip and changes", func(t *testing.T) {
		subject := uuid.NewString()
		if err := store.InTenantTx(ctx, func(tx pgx.Tx) error {
			return Audit(ctx, tx, AuditEntry{Action: "order.transition", SubjectType: "order",
				SubjectID: subject, Before: map[string]string{"status": "paid"},
				After: map[string]string{"status": "processing"}})
		}); err != nil {
			t.Fatalf("audit: %v", err)
		}
		if n := count(subject); n != 1 {
			t.Fatalf("%d rows, want 1", n)
		}

		var actor uuid.UUID
		var ip netip.Addr
		var tenantOnRow uuid.UUID
		var before, after string
		if err := store.InTenantTx(ctx, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT tenant_id, actor_id, ip, before::text, after::text
				FROM audit_log WHERE subject_id = $1`, subject).Scan(&tenantOnRow, &actor, &ip, &before, &after)
		}); err != nil {
			t.Fatalf("read row: %v", err)
		}
		if tenantOnRow != tenantID || actor != userID || ip.String() != "203.0.113.7" {
			t.Errorf("row has tenant %s actor %s ip %s", tenantOnRow, actor, ip)
		}
		if before != `{"status": "paid"}` || after != `{"status": "processing"}` {
			t.Errorf("before %s, after %s", before, after)
		}
	})

	t.Run("rollback leaves no row", func(t *testing.T) {
		subject := uuid.NewString()
		boom := errors.New("the write failed after auditing")
		err := store.InTenantTx(ctx, func(tx pgx.Tx) error {
			if err := Audit(ctx, tx, AuditEntry{Action: "brand.create", SubjectType: "brand", SubjectID: subject}); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("got %v, want the write's error", err)
		}
		if n := count(subject); n != 0 {
			t.Errorf("%d rows after rollback, want 0", n)
		}
	})

	t.Run("a system job records a NULL actor", func(t *testing.T) {
		subject := uuid.NewString()
		sysCtx := tenant.NewContext(t.Context(), tenantID)
		if err := store.InTenantTx(sysCtx, func(tx pgx.Tx) error {
			return Audit(sysCtx, tx, AuditEntry{Action: "channel.import", SubjectType: "channel", SubjectID: subject})
		}); err != nil {
			t.Fatalf("audit: %v", err)
		}
		var actor *uuid.UUID
		if err := store.InTenantTx(sysCtx, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT actor_id FROM audit_log WHERE subject_id = $1`, subject).Scan(&actor)
		}); err != nil {
			t.Fatalf("read row: %v", err)
		}
		if actor != nil {
			t.Errorf("actor %s, want NULL", actor)
		}
	})
}
