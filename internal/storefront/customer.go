package storefront

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/httpkit"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

type customerCtx struct{}

// customerFrom is the signed-in customer, when the request carried a valid
// customer token for the key's shop.
func customerFrom(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(customerCtx{}).(uuid.UUID)
	return id, ok
}

// customerAuth reads an optional customer token (BR-086, BR-093). Absent is
// fine: most routes are for guests too. Present means it must be a valid
// storefront token for the key's own shop -- a staff token, an expired one,
// or one from another shop is 401, never quietly treated as a guest.
//
// The routes that hand out tokens ignore it, so a stale access token cannot
// stand between a customer and a new one.
func customerAuth(signer *auth.Signer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodOptions || r.Header.Get("Authorization") == "" || signsIn(r) {
				next.ServeHTTP(w, r)
				return
			}
			raw, _ := httpkit.BearerToken(r)
			claims, err := signer.ParseCustomer(raw)
			shop, _ := tenant.FromContext(r.Context())
			if err != nil || claims.TenantID != shop {
				httpkit.WriteError(w, r, apperrors.CustomerAuthRequired(
					"The customer token is invalid, has expired, or is for another shop. Refresh it or sign in again."))
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), customerCtx{}, claims.UserID())))
		})
	}
}

// signsIn is whether r is one of the routes that issue tokens.
func signsIn(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	switch strings.TrimPrefix(r.URL.Path, BaseURL) {
	case "/customers", "/auth/login", "/auth/refresh", "/auth/google",
		"/auth/password-reset", "/auth/password-reset/confirm":
		return true
	}
	return false
}

// requireCustomer is a route that answers only a signed-in customer (BR-082),
// the storefront's counterpart of requirePermission.
func requireCustomer(next func(w http.ResponseWriter, r *http.Request, customerID uuid.UUID)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := customerFrom(r.Context())
		if !ok {
			httpkit.WriteError(w, r, apperrors.CustomerAuthRequired("Sign in: this needs the customer's access token."))
			return
		}
		next(w, r, id)
	}
}
