package storefront

import (
	"encoding/hex"
	"strings"
	"testing"
)

// TestConfig is P1-224's acceptance (04-api-spec.md §11.4; BR-122, BR-127,
// BR-129): what a website needs, with nothing secret in it.
func TestConfig(t *testing.T) {
	store := openStore(t)
	tenantID, key := seedKey(t, store, "https://a.example")
	t.Cleanup(func() { cleanCatalog(t, store, tenantID) })
	s := newShop(t, store, key)

	// No settings row yet: the defaults.
	code, c, raw := s.get(t, "/v1/storefront/config")
	if code != 200 || c["shop_name"] != "Storefront" || c["google_client_id"] != nil || c["midtrans"] != nil ||
		len(c["payment_methods"].([]any)) != 1 || c["payment_methods"].([]any)[0] != "bank_transfer" {
		t.Fatalf("defaults: %d %s", code, raw)
	}

	sealed, err := testBox(t).Seal(tenantID, []byte("Mid-server-CONFIG-SECRET"))
	if err != nil {
		t.Fatal(err)
	}
	execTenant(t, store, tenantID, `INSERT INTO storefront_settings (tenant_id, midtrans_webhook_id, google_client_id,
		bank_transfer_instructions, midtrans_enabled, midtrans_environment, midtrans_client_key, midtrans_server_key)
		VALUES (current_setting('app.tenant_id')::uuid, 'aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee', '1234-abc.apps.googleusercontent.com',
		        'Transfer to BCA 123', true, 'sandbox', 'Mid-client-abc', '\x`+hex.EncodeToString(sealed)+`')`)
	code, c, raw = s.get(t, "/v1/storefront/config")
	mt, _ := c["midtrans"].(map[string]any)
	if code != 200 || c["google_client_id"] != "1234-abc.apps.googleusercontent.com" || len(c["payment_methods"].([]any)) != 2 ||
		mt["client_key"] != "Mid-client-abc" || mt["snap_js_url"] != "https://app.sandbox.midtrans.com/snap/snap.js" {
		t.Fatalf("configured: %d %s", code, raw)
	}
	for _, secret := range []string{"CONFIG-SECRET", "server", "aaaaaaaa-bbbb", "webhook"} {
		if strings.Contains(raw, secret) {
			t.Errorf("config carries %q (BR-129)", secret)
		}
	}
}
