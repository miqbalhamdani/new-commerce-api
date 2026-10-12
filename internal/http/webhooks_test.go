package httpapi_test

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
)

func init() {
	isolationCases = append(isolationCases,
		// A body signed with A's key, sent to B's webhook: B's key does not
		// verify it, so it is 401 and B is untouched (BR-003, BR-124).
		isolationCase{route: route{"POST", "/v1/webhooks/midtrans/{webhook_id}"}, seed: seedMidtransWebhook,
			request: func(t *testing.T, s seeded) *http.Request {
				return httptest.NewRequest(http.MethodPost, "/v1/webhooks/midtrans/"+s.otherID,
					strings.NewReader(string(signedNotification("ORD-1", "Mid-server-"+s.tenantID))))
			}},
	)
}

// seedMidtransWebhook is a shop taking Midtrans with its own sealed key,
// "Mid-server-<tenant id>"; s.id is its webhook id.
func seedMidtransWebhook(ctx context.Context, t *testing.T, store *db.Store, tenantID uuid.UUID) seeded {
	t.Helper()
	s := seedSignedOwner(ctx, t, store, tenantID)
	sealed, err := testBox(t).Seal(tenantID, []byte("Mid-server-"+tenantID.String()))
	if err != nil {
		t.Fatal(err)
	}
	hook := uuid.New()
	execAs(t, store, tenantID, `INSERT INTO storefront_settings (tenant_id, midtrans_webhook_id, midtrans_enabled,
		midtrans_environment, midtrans_client_key, midtrans_server_key)
		VALUES (current_setting('app.tenant_id')::uuid, '`+hook.String()+`', true, 'sandbox', 'Mid-client',
		        '\x`+hex.EncodeToString(sealed)+`')`)
	s.id = hook.String()
	return s
}

func signedNotification(orderID, key string) []byte {
	sum := sha512.Sum512([]byte(orderID + "200" + "1000.00" + key))
	b, _ := json.Marshal(map[string]any{"order_id": orderID, "status_code": "200", "gross_amount": "1000.00",
		"transaction_status": "settlement", "signature_key": hex.EncodeToString(sum[:])})
	return b
}

// TestMidtransWebhookRoute: Midtrans reaches the route with no token, and
// anything it cannot prove is refused before a tenant is ever set.
func TestMidtransWebhookRoute(t *testing.T) {
	ctx := t.Context()
	store := openAppStore(ctx, t)
	tenantID := uuid.Must(uuid.NewV7())
	s := seedMidtransWebhook(ctx, t, store, tenantID)
	srv := newServer(t)
	send := func(path string, body []byte) (int, map[string]any) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body))))
		var p map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		return rec.Code, p
	}
	good := signedNotification("ORD-1", "Mid-server-"+tenantID.String())

	code, p := send("/v1/webhooks/midtrans/"+uuid.NewString(), good)
	assertProblem(t, code, p, 404, "not_found", "")
	code, p = send("/v1/webhooks/midtrans/not-a-uuid", good)
	assertProblem(t, code, p, 404, "not_found", "")
	code, p = send("/v1/webhooks/midtrans/"+s.id, signedNotification("ORD-1", "Mid-server-forged"))
	assertProblem(t, code, p, 401, "unauthenticated", "")
	code, p = send("/v1/webhooks/midtrans/"+s.id, []byte("not json"))
	assertProblem(t, code, p, 401, "unauthenticated", "")
	// Signed by the shop's key, for an order id that is none of its attempts:
	// 200, and Midtrans is never asked.
	if code, p = send("/v1/webhooks/midtrans/"+s.id, good); code != 200 {
		t.Errorf("a verified notification for an unknown order: %d %v", code, p)
	}
}
