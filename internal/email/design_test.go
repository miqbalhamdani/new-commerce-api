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

// TestInvitationDesign: the subject as before; the role as a badge, who
// invited, who to ask; the reply address only when the shop has one.
func TestInvitationDesign(t *testing.T) {
	m := Staff{Shop: "Toko <ABC>", Name: "Rizky", Link: "http://admin/accept-invite#token=inv_x",
		Inviter: "Budi Santoso", InviterEmail: "budi@tokoabc.com", Role: "ops", ReplyTo: "halo@tokoabc.com"}
	subject, text, html, err := Invitation(m)
	if err != nil {
		t.Fatal(err)
	}
	if subject != "Budi Santoso invited you to Toko <ABC>" {
		t.Errorf("subject %q", subject)
	}
	mustContain(t, "text", text, "YOU'RE INVITED TO TOKO <ABC>", "Hi Rizky,", "Your role:   Ops",
		"Invited by:  Budi Santoso (budi@tokoabc.com)", "inv_x", "ask Budi for a new invitation",
		"reach the shop at halo@tokoabc.com.")
	mustContain(t, "html", html, "You&#39;re invited to Toko &lt;ABC&gt;", ">Ops</td>",
		`href="mailto:budi@tokoabc.com"`, `href="http://admin/accept-invite#token=inv_x"`,
		"Set your password", "ask Budi for a new invitation", `href="mailto:halo@tokoabc.com"`, "<!--[if mso]>")

	m.ReplyTo = ""
	_, text, html, _ = Invitation(m)
	if strings.Contains(text, "Reply to this email") || strings.Contains(html, "Reply to this email") {
		t.Errorf("a shop with no reply address still offers one")
	}
	mustContain(t, "html", html, "Sent by Belli.id on behalf of Toko &lt;ABC&gt;.")
}

// TestStaffResetDesign: the staff reset names the admin account and its
// address; a customer's reset is not this email.
func TestStaffResetDesign(t *testing.T) {
	m := Staff{Shop: "Toko <ABC>", Name: "Rizky", Address: "rizky@example.com",
		Link: "http://admin/reset-password#token=rst_x"}
	subject, text, html, err := StaffPasswordReset(m)
	if err != nil {
		t.Fatal(err)
	}
	if subject != "Reset your Toko <ABC> password" {
		t.Errorf("subject %q", subject)
	}
	mustContain(t, "text", text, "RESET YOUR PASSWORD", "Toko <ABC> admin account (rizky@example.com)", "rst_x",
		"works once and expires in 30 minutes", "Sent by Belli.id on behalf of Toko <ABC>.")
	mustContain(t, "html", html, "Reset your password", "admin account (rizky@example.com)",
		`href="http://admin/reset-password#token=rst_x"`, ">Reset password</a>", "Toko &lt;ABC&gt;", "<!--[if mso]>")
	if strings.Contains(html, "Reply to this email") {
		t.Errorf("no reply address, yet the footer offers one")
	}
	if _, customer, _ := PasswordReset("Toko ABC", "Rina", "http://shop/reset"); strings.Contains(customer, "admin account") {
		t.Errorf("a customer's reset speaks of an admin account")
	}
}

// TestPreviewEmails writes the designed emails to EMAIL_PREVIEW_DIR, to look
// at in a browser; skipped otherwise.
func TestPreviewEmails(t *testing.T) {
	dir := os.Getenv("EMAIL_PREVIEW_DIR")
	if dir == "" {
		t.Skip("EMAIL_PREVIEW_DIR is unset")
	}
	m := Staff{Shop: "Toko Sinar Jaya", Name: "Rizky", Code: "482913", Link: "https://admin.belli.id/accept-invite#token=4f9c2a7e",
		Inviter: "Dewi Lestari", InviterEmail: "dewi@sinarjaya.id", Role: "admin", ReplyTo: "halo@sinarjaya.id",
		Address: "rizky.pratama@gmail.com"}
	previews := map[string]func(Staff) (string, string, string, error){"signup": Signup, "invitation": Invitation,
		"staff_reset": StaffPasswordReset}
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
