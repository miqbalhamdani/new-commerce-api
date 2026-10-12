package storefront

import (
	"net/http"
	"strings"
	"time"

	"github.com/miqbalhamdani/new-commerce-api/internal/httpkit"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
	"github.com/miqbalhamdani/new-commerce-api/internal/ratelimit"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// The storefront's limits (BR-014). Per-IP limits are loose on purpose:
// Indonesian carriers put many shoppers behind one address (CGNAT).
const (
	browserPerIP  = 120
	browserPerKey = 3000
	serverPerKey  = 600 // per key per IP
	checkoutPerIP = 10
)

// limits counts every request after the key middleware against its caller's
// limits: a browser (it sent Origin) per IP and per key, the website's server
// per key per IP, and a checkout per IP besides. Every response carries the
// RateLimit-* headers; over a limit is 429 rate_limited.
//
// ponytail: no burst allowance on the server limit (BR-014's "burst 100"); a
// sliding window already lets a quiet minute's budget go at once. Add a token
// bucket if a website's server proves to need it.
func limits(l *ratelimit.Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if l == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, ip := KeyFromContext(r.Context()), httpkit.ClientIP(r).String()
			var rules []ratelimit.Rule
			if key.Browser {
				rules = []ratelimit.Rule{
					{Key: "rl:sf:ip:" + ip, Limit: browserPerIP, Window: time.Minute},
					{Key: "rl:sf:key:" + key.ID.String(), Limit: browserPerKey, Window: time.Minute},
				}
			} else {
				rules = []ratelimit.Rule{{Key: "rl:sf:srv:" + key.ID.String() + ":" + ip, Limit: serverPerKey, Window: time.Minute}}
			}
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/checkout") {
				rules = append(rules, ratelimit.Rule{Key: "rl:sf:checkout:" + ip, Limit: checkoutPerIP, Window: time.Minute})
			}
			res := l.Check(r.Context(), rules...)
			res.Write(w.Header())
			if !res.Allowed {
				httpkit.WriteError(w, r, apperrors.RateLimited())
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// signInAllowed counts a sign-in, registration or password reset against
// BR-014's login limits -- per email within this shop when there is one, and
// per IP -- and answers the 429 itself when over.
func (s *Server) signInAllowed(w http.ResponseWriter, r *http.Request, email string) bool {
	if s.limits == nil {
		return true
	}
	shop, _ := tenant.FromContext(r.Context())
	ip := httpkit.ClientIP(r).String()
	rules := ratelimit.Login("shop:"+shop.String(), strings.ToLower(strings.TrimSpace(email)), ip)
	if email == "" {
		rules = rules[1:] // the IP rule alone: the token names no one yet
	}
	res := s.limits.Check(r.Context(), rules...)
	res.Write(w.Header())
	if !res.Allowed {
		httpkit.WriteError(w, r, apperrors.RateLimited())
	}
	return res.Allowed
}
