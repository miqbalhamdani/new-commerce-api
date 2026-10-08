package email

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/textproto"
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

// TestSMTP: what Mailpit receives -- the shop as sender, Reply-To, and both
// parts with the invitation link intact (quoted-printable must not mangle
// "token=").
func TestSMTP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	data := make(chan string, 1)
	go fakeSMTP(ln, data)

	link := "http://admin/accept-invite#token=inv_x.y"
	err = SMTP{Addr: ln.Addr().String(), Domain: "tokoabc.com"}.Send(context.Background(), Message{
		FromName: "Toko ABC", To: "rina@example.com", ReplyTo: "owner@tokoabc.com", Subject: "Budi invited you",
		Text: "Accept: " + link, HTML: `<a href="` + link + `">Accept</a>`})
	if err != nil {
		t.Fatal(err)
	}

	msg, err := mail.ReadMessage(strings.NewReader(<-data))
	if err != nil {
		t.Fatal(err)
	}
	if h := msg.Header; h.Get("From") != `"Toko ABC" <no-reply@tokoabc.com>` || h.Get("Reply-To") != "owner@tokoabc.com" ||
		h.Get("Subject") != "Budi invited you" {
		t.Errorf("headers %v", h)
	}
	_, params, _ := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	parts := multipart.NewReader(msg.Body, params["boundary"])
	for _, kind := range []string{"text/plain", "text/html"} {
		p, err := parts.NextPart()
		if err != nil {
			t.Fatalf("%s part: %v", kind, err)
		}
		b, _ := io.ReadAll(p)
		if !strings.HasPrefix(p.Header.Get("Content-Type"), kind) || !strings.Contains(string(b), link) {
			t.Errorf("%s part: %q", kind, b)
		}
	}
}

// fakeSMTP accepts one message and sends its DATA on data.
func fakeSMTP(ln net.Listener, data chan<- string) {
	c, err := ln.Accept()
	if err != nil {
		return
	}
	defer func() { _ = c.Close() }()
	tp := textproto.NewConn(c)
	_ = tp.PrintfLine("220 fake")
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		switch strings.ToUpper(strings.SplitN(line, " ", 2)[0]) {
		case "DATA":
			_ = tp.PrintfLine("354 go on")
			b, _ := tp.ReadDotBytes()
			data <- string(b)
			_ = tp.PrintfLine("250 ok")
		case "QUIT":
			_ = tp.PrintfLine("221 bye")
			return
		default:
			_ = tp.PrintfLine("250 ok")
		}
	}
}
