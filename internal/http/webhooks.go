package httpapi

import (
	"io"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

// MidtransNotification is Midtrans reporting a payment (P1-221, §11.10). No
// token and no permission: the route is in unauthenticated(), and the
// signature with the shop's own key is the credential (BR-003, BR-124). The
// body is read raw, never through decodeJSON: it is Midtrans', and the
// fields it adds are not a client's mistake.
func (s *Server) MidtransNotification(w http.ResponseWriter, r *http.Request, webhookID openapi_types.UUID) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		writeError(w, r, apperrors.ValidationFailed("The notification could not be read.").WithCause(err))
		return
	}
	if err := s.orders.Notify(r.Context(), webhookID, raw); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}
