package httpapi

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

// Admin session limit (BR-014): 100 requests a minute per user.
const (
	AdminRateLimit  = 100
	AdminRateWindow = time.Minute
)

// WindowCounter counts hits in a trailing window. queue.Client implements it
// against Redis; declared here so the limiter does not care which.
type WindowCounter interface {
	SlidingWindow(ctx context.Context, key string, limit int, window time.Duration) (allowed bool, count int, resetIn time.Duration, err error)
}

// RateLimiter enforces the admin limit per user (BR-014).
//
// Redis is the shared counter across replicas. When it errors the limiter
// falls back to a count kept in this process (01-product-requirements.md
// §8.3), so a Redis outage loosens the limit rather than taking admin down.
type RateLimiter struct {
	counter WindowCounter
	limit   int
	window  time.Duration
	local   localWindows
}

func NewRateLimiter(counter WindowCounter, limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{counter: counter, limit: limit, window: window,
		local: localWindows{hits: map[string]*localWindow{}}}
}

// Middleware must run after Authenticate: it keys on the user it put in the
// context. Requests with no user (login, refresh) pass untouched; their limits
// are per email and IP and arrive with P1-209.
func (l *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := auth.UserIDFromContext(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		key := "rl:admin:user:" + userID.String()

		allowed, count, resetIn, err := l.counter.SlidingWindow(r.Context(), key, l.limit, l.window)
		if err != nil {
			slog.WarnContext(r.Context(), "rate limit falling back to per-process", "error", err)
			allowed, count, resetIn = l.local.hit(key, l.limit, l.window, time.Now())
		}

		reset := max(int(math.Ceil(resetIn.Seconds())), 1)
		h := w.Header()
		h.Set("RateLimit-Limit", strconv.Itoa(l.limit))
		h.Set("RateLimit-Remaining", strconv.Itoa(max(l.limit-count, 0)))
		h.Set("RateLimit-Reset", strconv.Itoa(reset))
		if !allowed {
			h.Set("Retry-After", strconv.Itoa(reset))
			writeError(w, r, apperrors.RateLimited())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ponytail: fixed window per process, never pruned. Each replica allows the
// full limit, and the map holds one entry per user seen since boot -- fine for
// a fallback that runs only while Redis is down; prune on a ticker if it ever
// runs for long.
type localWindows struct {
	mu   sync.Mutex
	hits map[string]*localWindow
}

type localWindow struct {
	start time.Time
	count int
}

func (lw *localWindows) hit(key string, limit int, window time.Duration, now time.Time) (bool, int, time.Duration) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	w, ok := lw.hits[key]
	if !ok || now.Sub(w.start) >= window {
		w = &localWindow{start: now}
		lw.hits[key] = w
	}
	resetIn := w.start.Add(window).Sub(now)
	if w.count >= limit {
		return false, w.count, resetIn
	}
	w.count++
	return true, w.count, resetIn
}
