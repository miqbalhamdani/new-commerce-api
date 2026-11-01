package storefront

import (
	"net/http"

	"github.com/miqbalhamdani/new-commerce-api/internal/httpkit"
	"github.com/miqbalhamdani/new-commerce-api/internal/midtrans"
	"github.com/miqbalhamdani/new-commerce-api/internal/team"
)

// GetConfig is 04-api-spec.md §11.4's /config (P1-224, P1-703, P1-802): public
// by design, read once by the website to render sign-in, payment and its look
// (BR-142, BR-143). Nothing secret.
func (s *Server) GetConfig(w http.ResponseWriter, r *http.Request) {
	c, err := s.team.StorefrontConfig(r.Context())
	if err != nil {
		httpkit.WriteError(w, r, err)
		return
	}
	out := StorefrontConfig{ShopName: c.ShopName, GoogleClientId: c.GoogleClientID,
		BankTransferInstructions: c.BankTransferInstructions, PaymentMethods: []StorefrontConfigPaymentMethods{},
		Theme: StorefrontConfigTheme(c.Theme), BrandColor: team.ResolveColor(c.Theme, c.BrandColor),
		LogoUrl: s.team.LogoURL(c.LogoR2Key), HeroUrl: s.team.LogoURL(c.HeroR2Key), WhatsappNumber: c.WhatsappNumber,
		Tagline: c.Tagline, Instagram: c.Instagram, Address: c.Address, OpeningHours: c.OpeningHours,
		ShopeeUrl: c.ShopeeUrl, TokopediaUrl: c.TokopediaUrl}
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
