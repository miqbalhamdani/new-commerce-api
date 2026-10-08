package httpapi

import (
	"errors"
	"net/http"

	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
)

// NewRouter mounts the generated routes under /v1 behind authentication.
//
// The registration itself comes from openapi.yaml via oapi-codegen -- no route
// in this file, and none hand-written anywhere else. An endpoint exists because
// the contract says so.
func NewRouter(srv ServerInterface, signer *auth.Signer, limiter *RateLimiter) http.Handler {
	r := chi.NewRouter()
	// Outermost, so a span exists before anything can fail. An error raised by
	// the authentication middleware still carries a trace id that resolves.
	// The limiter keys on the user Authenticate puts in the context.
	r.Use(tracing, Authenticate(signer), limiter.Middleware)
	return HandlerWithOptions(srv, ChiServerOptions{
		BaseURL: "/v1", BaseRouter: r, ErrorHandlerFunc: paramError,
	})
}

// paramError answers a path, query or header parameter the generated binder
// could not take. An id that is not a UUID names no row, so it is the same 404
// as a row that does not exist; anything else is 422 naming the parameter.
func paramError(w http.ResponseWriter, r *http.Request, err error) {
	name := paramName(err)
	if name == "id" {
		writeError(w, r, apperrors.NotFound("No such resource."))
		return
	}
	writeError(w, r, apperrors.ValidationFailed(err.Error()).
		WithFields(apperrors.Field{Name: name, Detail: "invalid or missing"}).WithCause(err))
}

func paramName(err error) string {
	var (
		format   *InvalidParamFormatError
		required *RequiredParamError
		header   *RequiredHeaderError
		decode   *UnmarshalingParamError
		many     *TooManyValuesForParamError
	)
	switch {
	case errors.As(err, &format):
		return format.ParamName
	case errors.As(err, &required):
		return required.ParamName
	case errors.As(err, &header):
		return header.ParamName
	case errors.As(err, &decode):
		return decode.ParamName
	case errors.As(err, &many):
		return many.ParamName
	}
	return "query"
}

// tracing starts a span per request and names it after the matched route
// pattern rather than the URL, so /v1/products/{id} is one operation instead of
// one per product.
func tracing(next http.Handler) http.Handler {
	return otelhttp.NewHandler(next, "",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			if route := chi.RouteContext(r.Context()); route != nil && route.RoutePattern() != "" {
				return r.Method + " " + route.RoutePattern()
			}
			return r.Method + " " + r.URL.Path
		}),
	)
}
