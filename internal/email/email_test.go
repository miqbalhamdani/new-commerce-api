package email

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestResend: the shop's name in From, no-reply at the domain, Reply-To, and
// an idempotency key so a redelivered envelope is not a second email (BR-128).
func TestResend(t *testing.T) {
	var got map[string]any
	var headers http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	defer srv.Close()

	err := Resend{APIKey: "re_test", Domain: "tokoabc.com", URL: srv.URL}.Send(context.Background(),
		Message{FromName: "Toko ABC", To: "rina@example.com", ReplyTo: "owner@tokoabc.com", Subject: "Hi", Text: "t", HTML: "h", Key: "1-0"})
	if err != nil {
		t.Fatal(err)
	}
	if got["from"] != `"Toko ABC" <no-reply@tokoabc.com>` || got["reply_to"] != "owner@tokoabc.com" ||
		got["to"].([]any)[0] != "rina@example.com" {
		t.Errorf("body %v", got)
	}
	if headers.Get("Authorization") != "Bearer re_test" || headers.Get("Idempotency-Key") != "1-0" {
		t.Errorf("headers %v", headers)
	}
}

func TestInvitationTemplate(t *testing.T) {
	subject, text, html := Invitation("Toko <ABC>", "Budi", "ops", "http://admin/accept-invite#token=inv_x")
	if subject != "Budi invited you to Toko <ABC>" || !strings.Contains(text, "inv_x") || !strings.Contains(text, "7 days") {
		t.Errorf("%s\n%s", subject, text)
	}
	if !strings.Contains(html, "Toko &lt;ABC&gt;") {
		t.Errorf("html not escaped: %s", html)
	}
}

func TestLogSenderPrintsTheLink(t *testing.T) {
	var buf bytes.Buffer
	_ = Log{Out: &buf}.Send(context.Background(), Message{To: "a@b.c", Subject: "S", Text: "link inv_x\n"})
	if !strings.Contains(buf.String(), "inv_x") {
		t.Errorf("%s", buf.String())
	}
}
