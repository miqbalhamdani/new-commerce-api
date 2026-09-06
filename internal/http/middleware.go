package httpapi

import (
	"net/http"
	"strings"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// Authenticate reads the bearer token and puts the tenant it names into the
// request context.
//
// This is the only place a tenant enters the system. API spec.md 1: derived
// from the token, never from a header, query parameter or body -- accepting it
// from the request would make cross-tenant access a matter of editing one.
//
// Routes that opt out of authentication in openapi.yaml (`security: []`) are
// listed in unauthenticated below. Everything else needs a token, and a route
// that forgets to say so fails closed: with no tenant in the context,
// InTenantTx returns ErrNoTenantContext rather than reading anything.
func Authenticate(signer *auth.Signer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if unauthenticated(r) {
				next.ServeHTTP(w, r)
				return
			}

			raw, ok := bearerToken(r)
			if !ok {
				writeProblem(w, r, http.StatusUnauthorized, "unauthenticated",
					"Unauthenticated", "A bearer token is required.")
				return
			}
			claims, err := signer.Parse(raw)
			if err != nil {
				writeProblem(w, r, http.StatusUnauthorized, "unauthenticated",
					"Unauthenticated", "The access token is invalid or has expired.")
				return
			}

			next.ServeHTTP(w, r.WithContext(
				tenant.NewContext(r.Context(), claims.TenantID)))
		})
	}
}

// unauthenticated mirrors `security: []` in openapi.yaml.
//
// A hand-kept list is a real risk: adding an endpoint here by mistake would
// expose it. It is kept short and explicit for that reason, and the isolation
// suite covers every route regardless of which side of this it falls on.
func unauthenticated(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case "/v1/auth/login", "/v1/auth/refresh":
		return true
	default:
		return false
	}
}

func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	// Case-insensitive: RFC 7235 makes the scheme name case-insensitive, and
	// clients do vary.
	if len(header) < 7 || !strings.EqualFold(header[:7], "bearer ") {
		return "", false
	}
	token := strings.TrimSpace(header[7:])
	return token, token != ""
}
