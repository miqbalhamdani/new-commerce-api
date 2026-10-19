package email

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mustContain fails for each part missing from s.
func mustContain(t *testing.T, what, s string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(s, p) {
			t.Errorf("%s lacks %q", what, p)
		}
	}
}

// TestSignupDesign: the code in the subject, the text and the code box; the
// link on the button and spelled out; the shop escaped; Outlook's fallback
// kept although html/template drops comments.
func TestSignupDesign(t *testing.T) {
	link := "http://admin/accept-invite#token=inv_x&signup=1"
	subject, text, html, err := Signup(Staff{Shop: "Toko <ABC>", Name: "Dewi", Code: "482913", Link: link})
	if err != nil {
		t.Fatal(err)
	}
	if subject != "482913 is your Belli.id code" {
		t.Errorf("subject %q", subject)
	}
	mustContain(t, "text", text, "Hi Dewi,", "setting up Toko <ABC>:", "    482913", link, "works for 7 days")
	mustContain(t, "html", html, "Confirm your email", `class="code"`, ">482913</div>",
		`href="http://admin/accept-invite#token=inv_x&amp;signup=1"`, "Confirm email &amp; set password",
		"Toko &lt;ABC&gt;", "<!--[if mso]>", "register a shop on Belli.id")
	if strings.Contains(html, "Sudirman") || strings.Contains(html, "ZgotmplZ") {
		t.Errorf("html has a sample address or a refused value")
	}
}

// TestPreviewEmails writes the designed emails to EMAIL_PREVIEW_DIR, to look
// at in a browser; skipped otherwise.
func TestPreviewEmails(t *testing.T) {
	dir := os.Getenv("EMAIL_PREVIEW_DIR")
	if dir == "" {
		t.Skip("EMAIL_PREVIEW_DIR is unset")
	}
	m := Staff{Shop: "Toko Sinar Jaya", Name: "Dewi", Code: "482913", Link: "https://admin.belli.id/accept-invite#token=4f9c2a7e"}
	previews := map[string]func(Staff) (string, string, string, error){"signup": Signup}
	for name, render := range previews {
		_, text, html, err := render(m)
		if err != nil {
			t.Fatal(err)
		}
		for ext, body := range map[string]string{".html": html, ".txt": text} {
			if err := os.WriteFile(filepath.Join(dir, name+ext), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}
