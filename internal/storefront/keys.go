package storefront

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/httpkit"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// KeyCacheTTL is how long a resolved key is trusted without asking the
// database again, and so how long a revoked key keeps working (BR-085).
const KeyCacheTTL = 60 * time.Second

// Keys resolves X-Api-Key to its shop, behind a per-process cache, and
// enforces the key's origin (04-api-spec.md §11.2).
type Keys struct {
	store *db.Store
	ttl   time.Duration
	now   func() time.Time

	mu    sync.Mutex
	cache map[string]cachedKey
}

type cachedKey struct {
	key db.APIKey
	at  time.Time
}

func NewKeys(store *db.Store) *Keys {
	return &Keys{store: store, ttl: KeyCacheTTL, now: time.Now, cache: map[string]cachedKey{}}
}

// Key is the API key a request came in with, for the routes that need more
// than its tenant: a reset link must point at the key's website (P1-207),
// and rate limits differ for browser and server callers (P1-209).
type Key struct {
	ID            uuid.UUID
	AllowedOrigin string
	// Browser is whether the request carried an Origin (BR-083).
	Browser bool
}

type keyCtx struct{}

// KeyFromContext is the request's API key. Every request past the middleware
// has one.
func KeyFromContext(ctx context.Context) Key {
	k, _ := ctx.Value(keyCtx{}).(Key)
	return k
}

// Middleware is §11.2: preflight for anyone, then a live key, then the
// key's origin, then the key's tenant on the context -- the only way a tenant
// enters a storefront request (BR-003).
func (k *Keys) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// On every response, including errors: a cache in front of the API must
		// never serve one origin's answer to another.
		w.Header().Add("Vary", "Origin")
		origin := r.Header.Get("Origin")

		// A preflight carries no key -- browsers never send custom headers on
		// one -- so it cannot be checked against a key's origin. It is answered
		// for anyone; the request it clears is where the origin is enforced.
		if r.Method == http.MethodOptions {
			preflight(w, origin)
			return
		}

		raw := r.Header.Get("X-Api-Key")
		if raw == "" {
			httpkit.WriteError(w, r, apperrors.InvalidAPIKey())
			return
		}
		key, err := k.resolve(r.Context(), auth.HashRefreshToken(raw))
		if errors.Is(err, db.ErrNotFound) || err == nil && key.RevokedAt != nil {
			httpkit.WriteError(w, r, apperrors.InvalidAPIKey())
			return
		}
		if err != nil {
			httpkit.WriteError(w, r, err)
			return
		}

		// A browser request from anywhere but the key's website is refused,
		// and without Access-Control-Allow-Origin, so the page that made it
		// cannot read even the refusal (BR-083). No Origin is the website's
		// own server, which is accepted.
		if origin != "" {
			if origin != key.AllowedOrigin {
				httpkit.WriteError(w, r, apperrors.OriginNotAllowed())
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			// So the page can read its limits (BR-014).
			w.Header().Set("Access-Control-Expose-Headers", "RateLimit-Limit, RateLimit-Remaining, RateLimit-Reset, Retry-After")
		}

		ctx := tenant.NewContext(r.Context(), key.TenantID)
		ctx = context.WithValue(ctx, keyCtx{}, Key{ID: key.ID, AllowedOrigin: key.AllowedOrigin, Browser: origin != ""})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// allowedHeaders are the request headers a storefront call may carry
// (§11.2); Content-Type is listed because a JSON body is not a "simple"
// request.
var allowedHeaders = strings.Join([]string{"X-Api-Key", "Authorization", "X-Order-Token", "Content-Type"}, ", ")

func preflight(w http.ResponseWriter, origin string) {
	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
	w.Header().Set("Access-Control-Allow-Headers", allowedHeaders)
	w.Header().Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
}

// resolve answers from the cache while the entry is fresh, else from the
// database. Only keys that exist are cached, revoked ones included: caching
// misses would let anyone grow the map without bound by sending random keys.
func (k *Keys) resolve(ctx context.Context, hash string) (db.APIKey, error) {
	k.mu.Lock()
	c, ok := k.cache[hash]
	k.mu.Unlock()
	if ok && k.now().Sub(c.at) < k.ttl {
		return c.key, nil
	}

	key, err := k.store.ResolveAPIKey(ctx, hash)
	if err != nil {
		return db.APIKey{}, err
	}
	k.mu.Lock()
	k.cache[hash] = cachedKey{key: key, at: k.now()}
	k.mu.Unlock()

	if key.RevokedAt == nil {
		// A stale last_used_at is not worth a failed request.
		if err := k.store.TouchAPIKey(ctx, key); err != nil {
			slog.WarnContext(ctx, "could not record API key use", "error", err)
		}
	}
	return key, nil
}
