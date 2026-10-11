package storefront

import (
	"net/http"

	"github.com/miqbalhamdani/new-commerce-api/internal/httpkit"
	"github.com/miqbalhamdani/new-commerce-api/internal/midtrans"
)

// GetConfig is 04-api-spec.md §11.4's /config (P1-224): public by design,
// read once by the website to render sign-in and payment. Nothing secret.
func (s *Server) GetConfig(w http.ResponseWriter, r *http.Request) {
	c, err := s.team.StorefrontConfig(r.Context())
	if err != nil {
		httpkit.WriteError(w, r, err)
		return
	}
	out := StorefrontConfig{ShopName: c.ShopName, GoogleClientId: c.GoogleClientID,
		BankTransferInstructions: c.BankTransferInstructions, PaymentMethods: []StorefrontConfigPaymentMethods{}}
	if c.BankTransferEnabled {
		out.PaymentMethods = append(out.PaymentMethods, "bank_transfer")
	}
	if c.MidtransEnabled && c.MidtransEnvironment != nil && c.MidtransClientKey != nil {
		out.PaymentMethods = append(out.PaymentMethods, "midtrans")
		out.Midtrans = &MidtransConfig{ClientKey: *c.MidtransClientKey,
			Environment: MidtransConfigEnvironment(*c.MidtransEnvironment), SnapJsUrl: midtrans.SnapJS(*c.MidtransEnvironment)}
	}
	httpkit.WriteJSON(w, http.StatusOK, out)
}
