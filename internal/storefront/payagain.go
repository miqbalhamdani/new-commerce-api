package storefront

import (
	"errors"
	"net/http"

	"github.com/miqbalhamdani/new-commerce-api/internal/httpkit"
	"github.com/miqbalhamdani/new-commerce-api/internal/orders"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

// PayAgain is a link to pay a pending Midtrans order (P1-222, §11.10;
// BR-126), for the shopper whose order it is: a signed-in customer for their
// own order, or a guest with its order token -- answered as /me/orders and
// the guest lookup answer (BR-082, BR-091).
func (s *Server) PayAgain(w http.ResponseWriter, r *http.Request, orderNumber OrderNumber) {
	v, err := s.orders.ByNumber(r.Context(), orderNumber)
	if err != nil && !errors.Is(err, orders.ErrNoSuchOrder) {
		httpkit.WriteError(w, r, err)
		return
	}
	found := err == nil
	if customerID, signed := customerFrom(r.Context()); signed {
		if !found || v.Order.CustomerID == nil || *v.Order.CustomerID != customerID {
			httpkit.WriteError(w, r, apperrors.NotFound("No such order."))
			return
		}
	} else {
		token := r.Header.Get("X-Order-Token")
		if !found || token == "" || !s.orderTokens.Valid(v.Order.TenantID, v.Order.ID, token) {
			httpkit.WriteError(w, r, apperrors.CustomerAuthRequired("Sign in, or send this order's X-Order-Token, from its checkout."))
			return
		}
	}
	link, err := s.orders.PayAgain(r.Context(), v.Order.ID)
	if err != nil {
		httpkit.WriteError(w, r, err)
		return
	}
	httpkit.WriteJSON(w, http.StatusOK, Payment{SnapToken: link.Token, RedirectUrl: link.RedirectURL, ExpiresAt: link.ExpiresAt})
}
