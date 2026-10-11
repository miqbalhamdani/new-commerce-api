package midtrans

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestClient: Snap is opened with the shop's server key as Basic auth, the
// notification URL in X-Override-Notification and the amount in whole rupiah;
// Get Status reads back what Midtrans confirms (BR-123, BR-124).
func TestClient(t *testing.T) {
	var seen *http.Request
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		switch r.URL.Path {
		case "/snap/v1/transactions":
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"token":"tok-1","redirect_url":"https://pay.test/tok-1"}`))
		case "/v2/SF-000001/status":
			_, _ = w.Write([]byte(`{"status_code":"200","transaction_status":"settlement","fraud_status":"accept","gross_amount":"309000.00","order_id":"SF-000001"}`))
		case "/v2/SF-404/status":
			_, _ = w.Write([]byte(`{"status_code":"404","status_message":"Transaction doesn't exist."}`))
		default:
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(srv.Close)
	c := &Client{Hosts: map[string]Hosts{"sandbox": {Snap: srv.URL, API: srv.URL}}, HTTP: srv.Client()}

	link, err := c.CreateSnap(t.Context(), "sandbox", "Mid-server-KEY", Snap{OrderID: "SF-000001", GrossAmount: 309000,
		Name: "Rina", Email: "rina@example.com", NotificationURL: "https://api.test/v1/webhooks/midtrans/w1", ExpiryHours: 24})
	if err != nil || link.Token != "tok-1" || link.RedirectURL != "https://pay.test/tok-1" {
		t.Fatalf("snap: %+v %v", link, err)
	}
	if want := "Basic " + base64.StdEncoding.EncodeToString([]byte("Mid-server-KEY:")); seen.Header.Get("Authorization") != want {
		t.Errorf("auth %q", seen.Header.Get("Authorization"))
	}
	if seen.Header.Get("X-Override-Notification") != "https://api.test/v1/webhooks/midtrans/w1" {
		t.Errorf("notification header %q", seen.Header.Get("X-Override-Notification"))
	}
	td := body["transaction_details"].(map[string]any)
	if td["order_id"] != "SF-000001" || td["gross_amount"] != float64(309000) || body["expiry"].(map[string]any)["duration"] != float64(24) {
		t.Errorf("snap body %v", body)
	}

	st, err := c.GetStatus(t.Context(), "sandbox", "Mid-server-KEY", "SF-000001")
	if err != nil || st.TransactionStatus != "settlement" || st.FraudStatus != "accept" || st.GrossAmount != "309000.00" {
		t.Errorf("status: %+v %v", st, err)
	}
	for what, call := range map[string]func() error{
		"an unknown transaction": func() error { _, err := c.GetStatus(t.Context(), "sandbox", "k", "SF-404"); return err },
		"a 500":                  func() error { _, err := c.GetStatus(t.Context(), "sandbox", "k", "SF-500"); return err },
		"an unknown environment": func() error { _, err := c.CreateSnap(t.Context(), "staging", "k", Snap{}); return err },
	} {
		if err := call(); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: %v, want ErrUnavailable", what, err)
		}
	}
	if SnapJS("sandbox") != "https://app.sandbox.midtrans.com/snap/snap.js" || !strings.HasPrefix(SnapJS("production"), "https://app.midtrans.com") {
		t.Error("snap.js URLs")
	}
}
