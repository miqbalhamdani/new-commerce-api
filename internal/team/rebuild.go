package team

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// BR-098: a static website's deploy hook, called once its shop's first
// unbuilt change is a minute old.

// RebuildHosts are where a deploy hook may point: Vercel, Cloudflare Pages
// and Netlify. Nothing else, so the API is never pointed at its own network.
var RebuildHosts = []string{"api.vercel.com", "api.cloudflare.com", "api.netlify.com"}

// RebuildDelay is how old a shop's first unbuilt change is before its hook
// is called; changes in between join that call.
const RebuildDelay = 60 * time.Second

// ValidRebuildHook reports whether u is a hook BR-098 accepts: https on one
// of RebuildHosts, port 443, no credentials, at most 500 characters.
func ValidRebuildHook(u string) bool {
	p, err := url.Parse(u)
	return err == nil && len(u) <= 500 && p.Scheme == "https" && p.User == nil &&
		(p.Port() == "" || p.Port() == "443") && slices.Contains(RebuildHosts, p.Hostname())
}

// Poster calls a hook and returns its HTTP status.
type Poster func(ctx context.Context, hook string) (int, error)

// PostHook is the real Poster: an empty POST with 10 seconds, following no
// redirect, so a hook cannot bounce the call to another host.
func PostHook(ctx context.Context, hook string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook, nil)
	if err != nil {
		return 0, err
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	_ = res.Body.Close()
	return res.StatusCode, nil
}

// HookRescan is how often the worker looks again for the shops that have a
// hook; one just set is called within this, plus RebuildDelay.
const HookRescan = 5 * time.Minute

// RebuildHooks checks the hooks each interval until ctx ends.
//
// ponytail: finding the shops with a hook reads every shop in its own
// transaction, so it runs every HookRescan, not every tick; a SECURITY
// DEFINER read of only those shops would let a new hook be called sooner.
func RebuildHooks(ctx context.Context, store *db.Store, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	var shops []uuid.UUID
	var scanned time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if now.Sub(scanned) >= HookRescan {
				found, err := HookShops(ctx, store)
				if err != nil {
					slog.WarnContext(ctx, "rebuild hooks", "error", err)
					continue
				}
				shops, scanned = found, now
			}
			if err := RebuildTick(ctx, store, shops, time.Now(), RebuildDelay, PostHook); err != nil {
				slog.WarnContext(ctx, "rebuild hooks", "error", err)
			}
		}
	}
}

// HookShops are the shops with a rebuild hook set.
func HookShops(ctx context.Context, store *db.Store) ([]uuid.UUID, error) {
	ids, err := store.TenantIDs(ctx)
	if err != nil {
		return nil, err
	}
	var out []uuid.UUID
	for _, id := range ids {
		tctx := tenant.NewContext(ctx, id)
		err := store.InTenantTx(tctx, func(tx pgx.Tx) error {
			st, err := sqlcgen.New(tx).GetStorefrontSettings(tctx)
			if err == nil && st.RebuildHookUrl != nil {
				out = append(out, id)
			}
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// RebuildTick calls the hook of each of shops whose first change since its
// last call is at least delay old at now. Not retried: the next change asks
// again.
func RebuildTick(ctx context.Context, store *db.Store, shops []uuid.UUID, now time.Time, delay time.Duration, post Poster) error {
	for _, id := range shops {
		tctx := tenant.NewContext(ctx, id)
		var hook *string
		err := store.InTenantTx(tctx, func(tx pgx.Tx) error {
			q := sqlcgen.New(tx)
			st, err := q.GetStorefrontSettings(tctx)
			if errors.Is(err, pgx.ErrNoRows) || err == nil && st.RebuildHookUrl == nil {
				return nil
			}
			if err != nil {
				return err
			}
			since := time.Time{}
			if st.RebuildLastAt != nil {
				since = *st.RebuildLastAt
			}
			first, err := q.FirstChangeSince(tctx, since)
			if errors.Is(err, pgx.ErrNoRows) || err == nil && now.Sub(first) < delay {
				return nil
			}
			if err != nil {
				return err
			}
			claimed, err := q.ClaimRebuild(tctx, sqlcgen.ClaimRebuildParams{At: &now, Seen: st.RebuildLastAt})
			if errors.Is(err, pgx.ErrNoRows) { // another worker took it
				return nil
			}
			hook = claimed
			return err
		})
		if err != nil {
			return err
		}
		if hook == nil || !ValidRebuildHook(*hook) {
			continue
		}
		var status *int32
		if code, err := post(ctx, *hook); err != nil {
			slog.WarnContext(ctx, "rebuild hook failed", "tenant_id", id, "error", err)
		} else {
			c := int32(code)
			status = &c
		}
		if err := store.InTenantTx(tctx, func(tx pgx.Tx) error {
			return sqlcgen.New(tx).SetRebuildStatus(tctx, sqlcgen.SetRebuildStatusParams{Status: status, At: &now})
		}); err != nil {
			return err
		}
	}
	return nil
}
