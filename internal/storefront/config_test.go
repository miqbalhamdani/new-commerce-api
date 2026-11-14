package storefront

import (
	"encoding/hex"
	"strings"
	"testing"
)

// TestConfig is P1-224's acceptance (04-api-spec.md §11.4; BR-122, BR-127,
// BR-129): what a website needs, with nothing secret in it. And P1-703's:
// the shop's look (BR-142). And P1-802's: the theme, always with a colour
// (BR-143).
func TestConfig(t *testing.T) {
	store := openStore(t)
	tenantID, key := seedKey(t, store, "https://a.example")
	t.Cleanup(func() { cleanCatalog(t, store, tenantID) })
	s := newShop(t, store, key)

	// No settings row yet: the defaults.
	code, c, raw := s.get(t, "/v1/storefront/config")
	if code != 200 || c["shop_name"] != "Storefront" || c["google_client_id"] != nil || c["midtrans"] != nil || c["logo_url"] != nil ||
		len(c["payment_methods"].([]any)) != 1 || c["payment_methods"].([]any)[0] != "bank_transfer" ||
		c["theme"] != "katalog" || c["brand_color"] != "#2f5a4c" || c["hero_url"] != nil || c["address"] != nil {
		t.Fatalf("defaults: %d %s", code, raw)
	}

	sealed, err := testBox(t).Seal(tenantID, []byte("Mid-server-CONFIG-SECRET"))
	if err != nil {
		t.Fatal(err)
	}
	execTenant(t, store, tenantID, `INSERT INTO storefront_settings (tenant_id, midtrans_webhook_id, google_client_id,
		bank_transfer_instructions, midtrans_enabled, midtrans_environment, midtrans_client_key, midtrans_server_key,
		logo_r2_key, whatsapp_number, tagline, theme, brand_color, instagram, hero_r2_key, address, opening_hours,
		shopee_url, tokopedia_url, license, document_requirements)
		VALUES (current_setting('app.tenant_id')::uuid, 'aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee', '1234-abc.apps.googleusercontent.com',
		        'Transfer to BCA 123', true, 'sandbox', 'Mid-client-abc', '\x`+hex.EncodeToString(sealed)+`',
		        current_setting('app.tenant_id') || '/shop/logo.png', '6281234567890', 'Sprei katun lokal', 'busana', '#7a3e52', 'tokoabc',
		        current_setting('app.tenant_id') || '/shop/hero.jpg', 'Jl. Braga No. 10, Bandung', 'Setiap hari 09.00–21.00',
		        'https://shopee.co.id/tokoabc', NULL, 'PPIU No. U.133 Tahun 2020', 'Paspor, KTP')`)
	code, c, raw = s.get(t, "/v1/storefront/config")
	mt, _ := c["midtrans"].(map[string]any)
	if code != 200 || c["google_client_id"] != "1234-abc.apps.googleusercontent.com" || len(c["payment_methods"].([]any)) != 2 ||
		mt["client_key"] != "Mid-client-abc" || mt["snap_js_url"] != "https://app.sandbox.midtrans.com/snap/snap.js" ||
		!strings.HasSuffix(c["logo_url"].(string), "/"+tenantID.String()+"/shop/logo.png") || c["whatsapp_number"] != "6281234567890" ||
		c["tagline"] != "Sprei katun lokal" || c["theme"] != "busana" || c["brand_color"] != "#7a3e52" || c["instagram"] != "tokoabc" ||
		!strings.HasSuffix(c["hero_url"].(string), "/"+tenantID.String()+"/shop/hero.jpg") || c["address"] != "Jl. Braga No. 10, Bandung" ||
		c["opening_hours"] != "Setiap hari 09.00–21.00" || c["shopee_url"] != "https://shopee.co.id/tokoabc" || c["tokopedia_url"] != nil ||
		c["license"] != "PPIU No. U.133 Tahun 2020" || c["document_requirements"] != "Paspor, KTP" || c["cancellation_policy"] != nil {
		t.Fatalf("configured: %d %s", code, raw)
	}
	// No colour picked: the theme's first.
	execTenant(t, store, tenantID, `UPDATE storefront_settings SET brand_color = NULL`)
	if _, c, raw = s.get(t, "/v1/storefront/config"); c["brand_color"] != "#6e4b3a" {
		t.Errorf("theme default colour: %s", raw)
	}
	for _, secret := range []string{"CONFIG-SECRET", "server", "aaaaaaaa-bbbb", "webhook"} {
		if strings.Contains(raw, secret) {
			t.Errorf("config carries %q (BR-129)", secret)
		}
	}
}
