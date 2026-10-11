package storefront

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/midtrans"
)

// fakeMidtrans answers Snap and Get Status as Midtrans does, for one sandbox.
// down makes Snap fail; status is what Get Status says per order id.
type fakeMidtrans struct {
	srv    *httptest.Server
	mu     sync.Mutex
	down   bool
	snaps  []map[string]any
	auths  []string
	hooks  []string
	status map[string]string // order id -> Get Status body
}

func newFakeMidtrans(t *testing.T) *fakeMidtrans {
	t.Helper()
	f := &fakeMidtrans{status: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.URL.Path == "/snap/v1/transactions":
			if f.down {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.snaps = append(f.snaps, body)
			f.auths = append(f.auths, r.Header.Get("Authorization"))
			f.hooks = append(f.hooks, r.Header.Get("X-Override-Notification"))
			id := body["transaction_details"].(map[string]any)["order_id"].(string)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"tok-` + id + `","redirect_url":"https://pay.test/` + id + `"}`))
		case strings.HasPrefix(r.URL.Path, "/v2/") && strings.HasSuffix(r.URL.Path, "/status"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v2/"), "/status")
			if f.down {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			body, ok := f.status[id]
			if !ok {
				body = `{"status_code":"404","status_message":"Transaction doesn't exist."}`
			}
			_, _ = w.Write([]byte(body))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeMidtrans) client() *midtrans.Client {
	return &midtrans.Client{Hosts: map[string]midtrans.Hosts{"sandbox": {Snap: f.srv.URL, API: f.srv.URL}}, HTTP: f.srv.Client()}
}

func (f *fakeMidtrans) setDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

func (f *fakeMidtrans) snapCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.snaps)
}

// takesMidtrans turns Midtrans on for a shop, sandbox, with a sealed server
// key, and returns the webhook id.
func takesMidtrans(t *testing.T, store *db.Store, tenantID uuid.UUID) uuid.UUID {
	t.Helper()
	sealed, err := testBox(t).Seal(tenantID, []byte("Mid-server-TEST"))
	if err != nil {
		t.Fatal(err)
	}
	hook := uuid.New()
	execTenant(t, store, tenantID, `INSERT INTO storefront_settings (tenant_id, midtrans_webhook_id)
		VALUES (current_setting('app.tenant_id')::uuid, '`+hook.String()+`')
		ON CONFLICT (tenant_id) DO UPDATE SET midtrans_webhook_id = '`+hook.String()+`'`,
		`UPDATE storefront_settings SET midtrans_enabled = true, midtrans_environment = 'sandbox',
		 midtrans_client_key = 'Mid-client-TEST', midtrans_server_key = '\x`+hex.EncodeToString(sealed)+`'`)
	return hook
}
