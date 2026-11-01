package httpapi

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
	"github.com/miqbalhamdani/new-commerce-api/internal/team"
)

// Storefront settings: 04-api-spec.md §4 (P1-216, P1-703, P1-802).

func (s *Server) GetStorefrontSettings(w http.ResponseWriter, r *http.Request) {
	requirePermission(auth.PermSettingsRead, func(w http.ResponseWriter, r *http.Request) {
		st, err := s.team.StorefrontSettings(r.Context())
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, s.storefrontSettingsOut(st))
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
			Theme                    optional[string]   `json:"theme"`
			BrandColor               optional[string]   `json:"brand_color"`
			LogoR2Key                optional[string]   `json:"logo_r2_key"`
			HeroR2Key                optional[string]   `json:"hero_r2_key"`
			WhatsappNumber           optional[string]   `json:"whatsapp_number"`
			Tagline                  optional[string]   `json:"tagline"`
			Instagram                optional[string]   `json:"instagram"`
			Address                  optional[string]   `json:"address"`
			OpeningHours             optional[string]   `json:"opening_hours"`
			ShopeeURL                optional[string]   `json:"shopee_url"`
			TokopediaURL             optional[string]   `json:"tokopedia_url"`
		}
		if !decodeJSON(w, r, &body, "midtrans_server_key_set", "midtrans_notification_url", "midtrans_webhook_id", "logo_url", "hero_url") {
			return
		}
		if err := rejectNull(map[string]bool{"shipping_couriers": body.ShippingCouriers.Null,
			"bank_transfer_enabled": body.BankTransferEnabled.Null, "midtrans_enabled": body.MidtransEnabled.Null,
			"theme": body.Theme.Null}); err != nil {
			writeError(w, r, err)
			return
		}
		p := team.StorefrontPatch{
			ContactEmail: change(body.ContactEmail), GoogleClientID: change(body.GoogleClientID),
			OriginPostalCode: change(body.OriginPostalCode), BankTransferInstructions: change(body.BankTransferInstructions),
			MidtransEnvironment: change(body.MidtransEnvironment), MidtransClientKey: change(body.MidtransClientKey),
			MidtransServerKey: change(body.MidtransServerKey),
			BrandColor:        change(body.BrandColor), LogoR2Key: change(body.LogoR2Key), HeroR2Key: change(body.HeroR2Key),
			WhatsappNumber: change(body.WhatsappNumber), Tagline: change(body.Tagline), Instagram: change(body.Instagram),
			Address: change(body.Address), OpeningHours: change(body.OpeningHours),
			ShopeeURL: change(body.ShopeeURL), TokopediaURL: change(body.TokopediaURL),
		}
		if body.Theme.Set {
			p.Theme = &body.Theme.Value
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
		writeJSON(w, http.StatusOK, s.storefrontSettingsOut(st))
	})(w, r)
}

func change(o optional[string]) team.Change[string] {
	if !o.Set || o.Null {
		return team.Change[string]{Set: o.Set}
	}
	return team.Change[string]{Set: true, Value: &o.Value}
}

func (s *Server) storefrontSettingsOut(st team.StorefrontSettings) StorefrontSettings {
	out := StorefrontSettings{
		GoogleClientId: st.GoogleClientID, OriginPostalCode: st.OriginPostalCode,
		BankTransferEnabled: st.BankTransferEnabled, BankTransferInstructions: st.BankTransferInstructions,
		MidtransEnabled: st.MidtransEnabled, MidtransClientKey: st.MidtransClientKey,
		MidtransServerKeySet:    st.MidtransServerKey != nil,
		MidtransNotificationUrl: config.APIURL() + "/v1/webhooks/midtrans/" + st.MidtransWebhookID.String(),
		ShippingCouriers:        make([]CourierCode, 0, len(st.ShippingCouriers)),
		Theme:                   ShopTheme(st.Theme), BrandColor: st.BrandColor,
		LogoR2Key: st.LogoR2Key, LogoUrl: s.team.LogoURL(st.LogoR2Key),
		HeroR2Key: st.HeroR2Key, HeroUrl: s.team.LogoURL(st.HeroR2Key),
		WhatsappNumber: st.WhatsappNumber, Tagline: st.Tagline, Instagram: st.Instagram,
		Address: st.Address, OpeningHours: st.OpeningHours, ShopeeUrl: st.ShopeeUrl, TokopediaUrl: st.TokopediaUrl,
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
