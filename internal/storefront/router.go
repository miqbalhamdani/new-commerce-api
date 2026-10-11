// Package storefront is the API a shop's own website is built on
// (/v1/storefront, 04-api-spec.md §11). Its routes are generated from
// contracts/storefront.yaml into gen.go.
//
// It never imports the admin API (internal/http), nor that it (P1-202):
// what both need is in internal/httpkit. Storefront and admin are different
// audiences with different credentials, and a shared handler is how a staff
// capability leaks to the public.
package storefront

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/catalog"
	"github.com/miqbalhamdani/new-commerce-api/internal/httpkit"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

// BaseURL is where the storefront is mounted.
const BaseURL = "/v1/storefront"

// NewRouter mounts the generated storefront routes behind the key middleware.
// As on the admin side, no route is registered by hand: a route exists
// because storefront.yaml says so.
func NewRouter(srv ServerInterface, keys *Keys) http.Handler {
	r := chi.NewRouter()
	// problem+json for the routes that do not exist too, never chi's text.
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpkit.WriteError(w, r, apperrors.NotFound("No such route."))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpkit.WriteError(w, r, apperrors.NotFound("No such route."))
	})
	routes := HandlerWithOptions(srv, ChiServerOptions{
		BaseURL: BaseURL, BaseRouter: r, ErrorHandlerFunc: paramError,
	})
	// Around the router rather than r.Use: chi runs Use middleware only once a
	// route exists, and the key check must hold for every request, routed or
	// not. Tracing outermost, so a refused key still has a trace id.
	return httpkit.Tracing(keys.Middleware(routes))
}

// paramError answers a path or query parameter the generated binder could not
// take. A cart id or slug that cannot parse names nothing, so it is the same
// 404 as one that does not exist; anything else is 422 naming the parameter.
func paramError(w http.ResponseWriter, r *http.Request, err error) {
	name := paramName(err)
	switch name {
	case "cart_id", "slug", "order_number":
		httpkit.WriteError(w, r, apperrors.NotFound("No such resource."))
		return
	}
	httpkit.WriteError(w, r, apperrors.ValidationFailed(err.Error()).
		WithFields(apperrors.Field{Name: name, Detail: "invalid or missing"}).WithCause(err))
}

func paramName(err error) string {
	var (
		format   *InvalidParamFormatError
		required *RequiredParamError
		decode   *UnmarshalingParamError
		many     *TooManyValuesForParamError
	)
	switch {
	case errors.As(err, &format):
		return format.ParamName
	case errors.As(err, &required):
		return required.ParamName
	case errors.As(err, &decode):
		return decode.ParamName
	case errors.As(err, &many):
		return many.ParamName
	}
	return "query"
}

// Server implements the generated ServerInterface: decode, call a service,
// encode, as on the admin side.
type Server struct {
	catalog *catalog.Service
}

// Services are the domain use cases the storefront composes, one field per
// domain package.
type Services struct {
	Catalog *catalog.Service
}

func NewServer(svc Services) *Server { return &Server{catalog: svc.Catalog} }
