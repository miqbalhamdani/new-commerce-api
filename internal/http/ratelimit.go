package httpapi

import (
	"net/http"
	"strings"
	"time"

	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
	"github.com/miqbalhamdani/new-commerce-api/internal/ratelimit"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// Admin session limit (BR-014): 100 requests a minute per user.
const (
	AdminRateLimit  = 100
	AdminRateWindow = time.Minute
)

// WindowCounter counts hits in a trailing window: ratelimit.Counter, named
// here for the admin router's callers.
type WindowCounter = ratelimit.Counter

// RateLimiter enforces the admin limit per user (BR-014), on the limiter the
// storefront shares (internal/ratelimit).
type RateLimiter struct {
	limits *ratelimit.Limiter
	limit  int
	window time.Duration
}

func NewRateLimiter(counter WindowCounter, limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{limits: ratelimit.New(counter), limit: limit, window: window}
}

// Middleware must run after Authenticate: it keys on the user it put in the
// context. Requests with no user (login, refresh, the Midtrans webhook) pass
// untouched; sign-in counts against its own limits in the handler.
func (l *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, ok := tenant.ActorFromContext(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		res := l.limits.Check(r.Context(), ratelimit.Rule{Key: "rl:admin:user:" + actor.UserID.String(), Limit: l.limit, Window: l.window})
		res.Write(w.Header())
		if !res.Allowed {
			writeError(w, r, apperrors.RateLimited())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// checkLogin counts a staff sign-in ("staff") or signup ("signup") against
// BR-014's login limits, writing the headers, and reports whether it may go
// ahead.
func (l *RateLimiter) checkLogin(w http.ResponseWriter, r *http.Request, scope, email string) bool {
	res := l.limits.Check(r.Context(), ratelimit.Login(scope, strings.ToLower(strings.TrimSpace(email)), clientIP(r).String())...)
	res.Write(w.Header())
	if !res.Allowed {
		writeError(w, r, apperrors.RateLimited())
	}
	return res.Allowed
}

// checkGoogle counts a staff Google sign-in or sign-up against the per-IP
// half of the login limit only: the token names nobody until it is checked
// (BR-014).
func (l *RateLimiter) checkGoogle(w http.ResponseWriter, r *http.Request) bool {
	res := l.limits.Check(r.Context(), ratelimit.Login("google", "", clientIP(r).String())[1:]...)
	res.Write(w.Header())
	if !res.Allowed {
		writeError(w, r, apperrors.RateLimited())
	}
	return res.Allowed
}
