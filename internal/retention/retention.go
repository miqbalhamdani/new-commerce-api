// Package retention is the nightly sweep (BR-096): expired open carts, with
// their items, and customer sessions that can never refresh again.
//
// It runs shop by shop, each inside that shop's own InTenantTx as app_user,
// so RLS holds as it does for a request and no role bypasses it (BR-003).
package retention

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// At is when the sweep runs, WIB (BR-007).
const At = 2 * time.Hour

// Claimer hands a key to the first caller to ask for it within ttl
// (queue.Client).
type Claimer interface {
	Claim(ctx context.Context, key string, ttl time.Duration) (bool, error)
}

// Nightly sweeps every day at 02:00 WIB until ctx ends. Every worker replica
// runs it; the one that claims the day sweeps.
//
// ponytail: a night when no worker is up is skipped, not caught up. Nothing
// is lost -- the next night deletes what that one would have.
func Nightly(ctx context.Context, store *db.Store, claims Claimer) {
	for {
		next := nextRun(time.Now())
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}
		ok, err := claims.Claim(ctx, "retention:"+next.Format(time.DateOnly), 23*time.Hour)
		if err != nil {
			slog.ErrorContext(ctx, "retention: could not claim tonight's sweep", "error", err)
			continue
		}
		if !ok {
			continue
		}
		if err := Sweep(ctx, store); err != nil {
			slog.ErrorContext(ctx, "retention sweep failed", "error", err)
		}
	}
}

// nextRun is the first 02:00 WIB after now.
func nextRun(now time.Time) time.Time {
	now = now.In(config.WIB)
	next := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, config.WIB).Add(At)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// Sweep cleans every shop. One shop failing does not stop the rest.
func Sweep(ctx context.Context, store *db.Store) error {
	ids, err := store.TenantIDs(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		if err := sweepShop(ctx, store, id); err != nil {
			errs = append(errs, fmt.Errorf("shop %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// sweepShop deletes one shop's expired carts and ended sessions, and records
// what it deleted in the shop's audit log, with no actor (BR-018). A night
// with nothing to delete records nothing.
func sweepShop(ctx context.Context, store *db.Store, tenantID uuid.UUID) error {
	ctx = tenant.NewContext(ctx, tenantID)
	return store.InTenantTx(ctx, func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		carts, err := q.DeleteExpiredCarts(ctx)
		if err != nil {
			return err
		}
		sessions, err := q.DeleteEndedSessions(ctx)
		if err != nil {
			return err
		}
		if carts+sessions == 0 {
			return nil
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "retention.sweep", SubjectType: "tenant",
			SubjectID: tenantID.String(), After: map[string]int64{"carts": carts, "customer_sessions": sessions}})
	})
}
