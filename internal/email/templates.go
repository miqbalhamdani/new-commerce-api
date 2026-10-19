package email

import (
	"embed"
	"fmt"
	"html"
	htmltemplate "html/template"
	"strings"
	texttemplate "text/template"
)

// The staff and owner emails are files in the Belli.id email design: an HTML
// part on templates/layout.html and a plain-text twin.
//
//go:embed templates
var files embed.FS

// msoHead is the web font for most clients and the Arial fallback for
// Outlook. html/template drops HTML comments, so these conditional comments
// go in as trusted markup.
const msoHead = `<!--[if !mso]><!-->
<link href="https://fonts.googleapis.com/css2?family=Outfit:wght@400;500;600;700&display=swap" rel="stylesheet">
<!--<![endif]-->
<!--[if mso]>
<noscript><xml><o:OfficeDocumentSettings><o:PixelsPerInch>96</o:PixelsPerInch></o:OfficeDocumentSettings></xml></noscript>
<style>table,td,a,span{font-family:Arial,Helvetica,sans-serif !important;}</style>
<![endif]-->`

type button struct {
	Link, Label string
	Spaced      bool
}

var funcs = htmltemplate.FuncMap{
	"mso": func() htmltemplate.HTML { return msoHead },
	"button": func(link, label string, spaced bool) button {
		return button{Link: link, Label: label, Spaced: spaced}
	},
}

// design is one email of the design: its HTML and its plain text.
type design struct {
	html *htmltemplate.Template
	text *texttemplate.Template
}

func parse(name string) design {
	return design{
		html: htmltemplate.Must(htmltemplate.New(name).Funcs(funcs).
			ParseFS(files, "templates/layout.html", "templates/"+name+".html")),
		text: texttemplate.Must(texttemplate.New(name+".txt").Funcs(texttemplate.FuncMap{"upper": strings.ToUpper}).
			ParseFS(files, "templates/"+name+".txt", "templates/shop.txt")),
	}
}

func (d design) render(m Staff) (text, htmlBody string, err error) {
	var t, h strings.Builder
	if err := d.text.Execute(&t, m); err != nil {
		return "", "", err
	}
	if err := d.html.ExecuteTemplate(&h, "layout", m); err != nil {
		return "", "", err
	}
	return t.String(), h.String(), nil
}

var (
	signup     = parse("signup")
	invitation = parse("invitation")
	staffReset = parse("staff_reset")
)

// Staff is what a staff or owner email says: the shop, who it is to, and
// the link (with a code, for a signup); who invited them, for an invitation;
// and where a reply goes, for mail from the shop.
type Staff struct {
	Shop         string
	Name         string // the recipient's
	Address      string // the recipient's email, to say which account
	Code         string
	Link         string
	Inviter      string
	InviterEmail string
	Role         string // owner, admin, ops or viewer
	ReplyTo      string // the shop's contact email; "" when it has none
}

// RoleLabel is the role as the Team screen names it: "Admin".
func (m Staff) RoleLabel() string {
	if m.Role == "" {
		return ""
	}
	return strings.ToUpper(m.Role[:1]) + m.Role[1:]
}

// InviterFirst is who to ask for a new invitation: "ask Dewi".
func (m Staff) InviterFirst() string {
	if f := strings.Fields(m.Inviter); len(f) > 0 {
		return f[0]
	}
	return "the shop"
}

// Signup confirms a new owner's email (P1-084, P1-086, BR-026, BR-128), in
// English (BR-016): the 6-digit code for the screen they are on, and the link
// for anyone who closed it.
func Signup(m Staff) (subject, text, htmlBody string, err error) {
	text, htmlBody, err = signup.render(m)
	return m.Code + " is your Belli.id code", text, htmlBody, err
}

// Invitation is the staff invitation (BR-026, BR-128), in English (BR-016).
func Invitation(m Staff) (subject, text, htmlBody string, err error) {
	text, htmlBody, err = invitation.render(m)
	return fmt.Sprintf("%s invited you to %s", m.Inviter, m.Shop), text, htmlBody, err
}

// StaffPasswordReset is a staff member's reset link (P1-088, BR-094,
// BR-128), in English (BR-016). Customers get PasswordReset.
func StaffPasswordReset(m Staff) (subject, text, htmlBody string, err error) {
	text, htmlBody, err = staffReset.render(m)
	return "Reset your " + m.Shop + " password", text, htmlBody, err
}

// PasswordReset is a customer's reset link (BR-094, BR-128), in English
// (BR-016).
func PasswordReset(shop, name, link string) (subject, text, htmlBody string) {
	subject = "Reset your " + shop + " password"
	text = fmt.Sprintf(`Hi %s,

Someone asked to reset the password of your %s account. If it was you, set a new one here:
%s

The link works once, for 30 minutes. If you did not ask, ignore this email: your password stays as it is.
`, name, shop, link)
	htmlBody = fmt.Sprintf(`<p>Hi %s,</p>
<p>Someone asked to reset the password of your <strong>%s</strong> account. If it was you,
<a href="%s">set a new password</a>.</p>
<p>The link works once, for 30 minutes. If you did not ask, ignore this email: your password stays as it is.</p>`,
		html.EscapeString(name), html.EscapeString(shop), html.EscapeString(link))
	return subject, text, htmlBody
}

// OrderLine is one line of an order email.
type OrderLine struct {
	Title string
	Qty   int
	Total string // already formatted
}

// OrderPlaced is "we have your order" (BR-128): the lines, the total, and how
// to pay when it is a bank transfer.
func OrderPlaced(shop, name, number string, lines []OrderLine, total, bankInstructions string) (subject, text, htmlBody string) {
	subject = fmt.Sprintf("Your %s order %s", shop, number)
	var t, h strings.Builder
	fmt.Fprintf(&t, "Hi %s,\n\nThank you for your order %s at %s.\n\n", name, number, shop)
	fmt.Fprintf(&h, "<p>Hi %s,</p>\n<p>Thank you for your order <strong>%s</strong> at %s.</p>\n<ul>\n",
		html.EscapeString(name), html.EscapeString(number), html.EscapeString(shop))
	for _, l := range lines {
		fmt.Fprintf(&t, "  %d × %s  %s\n", l.Qty, l.Title, l.Total)
		fmt.Fprintf(&h, "<li>%d × %s — %s</li>\n", l.Qty, html.EscapeString(l.Title), html.EscapeString(l.Total))
	}
	fmt.Fprintf(&t, "\nTotal: %s\n", total)
	fmt.Fprintf(&h, "</ul>\n<p>Total: <strong>%s</strong></p>\n", html.EscapeString(total))
	if bankInstructions != "" {
		fmt.Fprintf(&t, "\nTo pay by bank transfer:\n%s\n", bankInstructions)
		fmt.Fprintf(&h, "<p>To pay by bank transfer:<br>%s</p>\n", html.EscapeString(bankInstructions))
	}
	return subject, t.String(), h.String()
}

// PaymentReceived is "we have your payment" (BR-128).
func PaymentReceived(shop, name, number, total string) (subject, text, htmlBody string) {
	subject = fmt.Sprintf("Payment received for %s", number)
	text = fmt.Sprintf("Hi %s,\n\nWe have your payment of %s for order %s. %s is getting it ready.\n", name, total, number, shop)
	htmlBody = fmt.Sprintf("<p>Hi %s,</p>\n<p>We have your payment of <strong>%s</strong> for order %s. %s is getting it ready.</p>",
		html.EscapeString(name), html.EscapeString(total), html.EscapeString(number), html.EscapeString(shop))
	return subject, text, htmlBody
}

// OrderShipped is "it is on its way", with the courier and the tracking
// number (BR-128).
func OrderShipped(shop, name, number, courier, tracking string) (subject, text, htmlBody string) {
	subject = fmt.Sprintf("Your %s order %s has shipped", shop, number)
	text = fmt.Sprintf("Hi %s,\n\nOrder %s is on its way with %s.\nTracking number: %s\n", name, number, courier, tracking)
	htmlBody = fmt.Sprintf("<p>Hi %s,</p>\n<p>Order %s is on its way with <strong>%s</strong>.</p>\n<p>Tracking number: <strong>%s</strong></p>",
		html.EscapeString(name), html.EscapeString(number), html.EscapeString(courier), html.EscapeString(tracking))
	return subject, text, htmlBody
}
