package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	httpapi "github.com/miqbalhamdani/new-commerce-api/internal/http"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/queue"
)

// TestAdminRateLimit is P1-015's acceptance against real Redis: 100 requests a
// minute pass with RateLimit-* headers, the 101st is 429 rate_limited with
// Retry-After (BR-014).
func TestAdminRateLimit(t *testing.T) {
	redis, err := queue.New(t.Context(), config.RedisURL())
	if err != nil {
		t.Fatalf("connect redis: %v\n\nIs it running?\n  brew services start redis", err)
	}
	t.Cleanup(func() { _ = redis.Close() })

	limiter := httpapi.NewRateLimiter(redis, httpapi.AdminRateLimit, httpapi.AdminRateWindow)
	assertLimits(t, limiter.Middleware(okHandler()), httpapi.AdminRateLimit)
}

// TestAdminRateLimitFallback: with Redis failing, the per-process window still
// limits rather than letting everything through or failing every request.
func TestAdminRateLimitFallback(t *testing.T) {
	limiter := httpapi.NewRateLimiter(brokenCounter{}, 5, time.Minute)
	assertLimits(t, limiter.Middleware(okHandler()), 5)
}

// TestRateLimitSkipsAnonymous: login and refresh carry no user; their limits
// are P1-209's, and this limiter must not key them all on one empty id.
func TestRateLimitSkipsAnonymous(t *testing.T) {
	limiter := httpapi.NewRateLimiter(brokenCounter{}, 1, time.Minute)
	h := limiter.Middleware(okHandler())
	for range 3 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/login", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("anonymous request: status %d, want 200", rec.Code)
		}
	}
}

func assertLimits(t *testing.T, h http.Handler, limit int) {
	t.Helper()
	ctx := auth.NewUserContext(context.Background(), uuid.New())

	for i := 1; i <= limit; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/roles", nil).WithContext(ctx))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d, want 200", i, rec.Code)
		}
		if got := rec.Header().Get("RateLimit-Remaining"); got != strconv.Itoa(limit-i) {
			t.Fatalf("request %d: RateLimit-Remaining %q, want %d", i, got, limit-i)
		}
		if rec.Header().Get("RateLimit-Limit") != strconv.Itoa(limit) || rec.Header().Get("RateLimit-Reset") == "" {
			t.Fatalf("request %d: missing RateLimit headers: %v", i, rec.Header())
		}
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/roles", nil).WithContext(ctx))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("request %d: status %d, want 429", limit+1, rec.Code)
	}
	if retry, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || retry < 1 {
		t.Errorf("Retry-After %q, want a positive integer", rec.Header().Get("Retry-After"))
	}
	if !strings.Contains(rec.Body.String(), "/errors/rate_limited") {
		t.Errorf("body does not name rate_limited: %s", rec.Body)
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

type brokenCounter struct{}

func (brokenCounter) SlidingWindow(context.Context, string, int, time.Duration) (bool, int, time.Duration, error) {
	return false, 0, 0, errors.New("redis is down")
}
