package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
)

// NewRouter mounts the generated routes under /v1 behind authentication.
//
// The registration itself comes from openapi.yaml via oapi-codegen -- no route
// in this file, and none hand-written anywhere else. An endpoint exists because
// the contract says so.
func NewRouter(srv ServerInterface, signer *auth.Signer) http.Handler {
	r := chi.NewRouter()
	r.Use(Authenticate(signer))
	return HandlerFromMuxWithBaseURL(srv, r, "/v1")
}
