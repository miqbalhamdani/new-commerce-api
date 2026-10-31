package httpapi

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/team"
)

// Storefront settings: 04-api-spec.md §4 (P1-216).

func (s *Server) GetStorefrontSettings(w http.ResponseWriter, r *http.Request) {
	requirePermission(auth.PermSettingsRead, func(w http.ResponseWriter, r *http.Request) {
		st, err := s.team.StorefrontSettings(r.Context())
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, storefrontSettingsOut(st, s.team.LogoURL(st.LogoR2Key)))
	})(w, r)
}

func (s *Server) UpdateStorefrontSettings(w http.ResponseWriter, r *http.Request) {
	requirePermission(auth.PermSettingsWrite, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ContactEmail             optional[string]   `json:"contact_email"`
			GoogleClientID           optional[string]   `json:"google_client_id"`
			OriginPostalCode         optional[string]   `json:"origin_postal_code"`
			ShippingCouriers         optional[[]string] `json:"shipping_couriers"`
			BankTransferEnabled      optional[bool]     `json:"bank_transfer_enabled"`
			BankTransferInstructions optional[string]   `json:"bank_transfer_instructions"`
			MidtransEnabled          optional[bool]     `json:"midtrans_enabled"`
			MidtransEnvironment      optional[string]   `json:"midtrans_environment"`
			MidtransClientKey        optional[string]   `json:"midtrans_client_key"`
			MidtransServerKey        optional[string]   `json:"midtrans_server_key"`
			LogoR2Key                optional[string]   `json:"logo_r2_key"`
			WhatsappNumber           optional[string]   `json:"whatsapp_number"`
			Tagline                  optional[string]   `json:"tagline"`
			BrandColor               optional[string]   `json:"brand_color"`
			Instagram                optional[string]   `json:"instagram"`
		}
		if !decodeJSON(w, r, &body, "midtrans_server_key_set", "midtrans_notification_url", "midtrans_webhook_id", "logo_url") {
			return
		}
		if err := rejectNull(map[string]bool{"shipping_couriers": body.ShippingCouriers.Null,
			"bank_transfer_enabled": body.BankTransferEnabled.Null, "midtrans_enabled": body.MidtransEnabled.Null}); err != nil {
			writeError(w, r, err)
			return
		}
		p := team.StorefrontPatch{
			ContactEmail: change(body.ContactEmail), GoogleClientID: change(body.GoogleClientID),
			OriginPostalCode: change(body.OriginPostalCode), BankTransferInstructions: change(body.BankTransferInstructions),
			MidtransEnvironment: change(body.MidtransEnvironment), MidtransClientKey: change(body.MidtransClientKey),
			MidtransServerKey: change(body.MidtransServerKey),
			LogoR2Key:         change(body.LogoR2Key), WhatsappNumber: change(body.WhatsappNumber), Tagline: change(body.Tagline),
			BrandColor: change(body.BrandColor), Instagram: change(body.Instagram),
		}
		if body.ShippingCouriers.Set {
			p.ShippingCouriers = &body.ShippingCouriers.Value
		}
		if body.BankTransferEnabled.Set {
			p.BankTransferEnabled = &body.BankTransferEnabled.Value
		}
		if body.MidtransEnabled.Set {
			p.MidtransEnabled = &body.MidtransEnabled.Value
		}
		st, err := s.team.UpdateStorefrontSettings(r.Context(), p)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, storefrontSettingsOut(st, s.team.LogoURL(st.LogoR2Key)))
	})(w, r)
}

func change(o optional[string]) team.Change[string] {
	if !o.Set || o.Null {
		return team.Change[string]{Set: o.Set}
	}
	return team.Change[string]{Set: true, Value: &o.Value}
}

func storefrontSettingsOut(st team.StorefrontSettings, logoURL *string) StorefrontSettings {
	out := StorefrontSettings{
		GoogleClientId: st.GoogleClientID, OriginPostalCode: st.OriginPostalCode,
		BankTransferEnabled: st.BankTransferEnabled, BankTransferInstructions: st.BankTransferInstructions,
		MidtransEnabled: st.MidtransEnabled, MidtransClientKey: st.MidtransClientKey,
		MidtransServerKeySet:    st.MidtransServerKey != nil,
		MidtransNotificationUrl: config.APIURL() + "/v1/webhooks/midtrans/" + st.MidtransWebhookID.String(),
		ShippingCouriers:        make([]CourierCode, 0, len(st.ShippingCouriers)),
		LogoR2Key:               st.LogoR2Key, LogoUrl: logoURL, WhatsappNumber: st.WhatsappNumber, Tagline: st.Tagline,
		BrandColor: st.BrandColor, Instagram: st.Instagram,
		UpdatedAt: st.UpdatedAt,
	}
	if st.ContactEmail != nil {
		e := openapi_types.Email(*st.ContactEmail)
		out.ContactEmail = &e
	}
	if st.MidtransEnvironment != nil {
		env := StorefrontSettingsMidtransEnvironment(*st.MidtransEnvironment)
		out.MidtransEnvironment = &env
	}
	for _, c := range st.ShippingCouriers {
		out.ShippingCouriers = append(out.ShippingCouriers, CourierCode(c))
	}
	return out
}
