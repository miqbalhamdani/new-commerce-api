// Package ratelimit counts requests against BR-014's limits, for the admin
// API and the storefront alike: a Redis sliding window shared by every
// replica, and a per-process fallback while Redis is down, so an outage
// loosens the limits rather than taking the API down (01-product-requirements
// §8.3).
package ratelimit

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Counter counts hits in a trailing window: queue.Client against Redis.
type Counter interface {
	SlidingWindow(ctx context.Context, key string, limit int, window time.Duration) (allowed bool, count int, resetIn time.Duration, err error)
}

// Rule is one limit: at most Limit hits on Key in any Window.
type Rule struct {
	Key    string
	Limit  int
	Window time.Duration
}

// Result is a request counted against its rules: whether every rule let it
// through, and the headers of the tightest one.
type Result struct {
	Allowed          bool
	Limit, Remaining int
	Reset            time.Duration
}

// Limiter counts requests.
type Limiter struct {
	counter Counter
	local   localWindows
}

func New(counter Counter) *Limiter {
	return &Limiter{counter: counter, local: localWindows{hits: map[string]*localWindow{}}}
}

// Check counts one hit against every rule. The request is allowed only if
// every rule allows it; the result reports the rule with the fewest left.
//
// ponytail: a request one rule refuses still counts against the rules that
// allowed it. Exact would be a check-all-then-record script across keys;
// worth it only if near-limit callers prove to be throttled early.
func (l *Limiter) Check(ctx context.Context, rules ...Rule) Result {
	res := Result{Allowed: true, Remaining: math.MaxInt}
	for _, r := range rules {
		allowed, count, resetIn, err := l.counter.SlidingWindow(ctx, r.Key, r.Limit, r.Window)
		if err != nil {
			slog.WarnContext(ctx, "rate limit falling back to per-process", "error", err)
			allowed, count, resetIn = l.local.hit(r.Key, r.Limit, r.Window, time.Now())
		}
		remaining := max(r.Limit-count, 0)
		if !allowed || remaining < res.Remaining {
			res.Limit, res.Remaining, res.Reset = r.Limit, remaining, resetIn
		}
		res.Allowed = res.Allowed && allowed
	}
	return res
}

// Write sets the RateLimit-* headers BR-014 puts on every response, and
// Retry-After when the request is refused.
func (r Result) Write(h http.Header) {
	if r.Limit == 0 {
		return
	}
	reset := strconv.Itoa(max(int(math.Ceil(r.Reset.Seconds())), 1))
	h.Set("RateLimit-Limit", strconv.Itoa(r.Limit))
	h.Set("RateLimit-Remaining", strconv.Itoa(r.Remaining))
	h.Set("RateLimit-Reset", reset)
	if !r.Allowed {
		h.Set("Retry-After", reset)
	}
}

// ponytail: fixed window per process, never pruned. Each replica allows the
// full limit, and the map holds one entry per key seen since boot -- fine for
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

// The login limits (BR-014), shared by staff and customer sign-in and by
// password reset: 10 per 15 minutes per email, 30 per 15 minutes per IP.
const (
	LoginPerEmail = 10
	LoginPerIP    = 30
	LoginWindow   = 15 * time.Minute
)

// Login is the two rules a sign-in or reset counts against. scope keeps one
// shop's customer emails apart from another's and from staff's.
func Login(scope, email, ip string) []Rule {
	return []Rule{
		{Key: "rl:login:email:" + scope + ":" + email, Limit: LoginPerEmail, Window: LoginWindow},
		{Key: "rl:login:ip:" + ip, Limit: LoginPerIP, Window: LoginWindow},
	}
}
