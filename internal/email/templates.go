package email

import (
	"fmt"
	"html"
	"strings"
)

// Invitation is the staff invitation (BR-026, BR-128), in English (BR-016).
func Invitation(shop, inviter, role, link string) (subject, text, htmlBody string) {
	subject = fmt.Sprintf("%s invited you to %s", inviter, shop)
	text = fmt.Sprintf(`%s invited you to manage %s as %s.

Set your password to get started:
%s

The link works for 7 days. If it has expired, ask %s to send a new invitation.
`, inviter, shop, role, link, inviter)
	htmlBody = fmt.Sprintf(`<p>%s invited you to manage <strong>%s</strong> as %s.</p>
<p><a href="%s">Set your password</a> to get started.</p>
<p>The link works for 7 days. If it has expired, ask %s to send a new invitation.</p>`,
		html.EscapeString(inviter), html.EscapeString(shop), html.EscapeString(role),
		html.EscapeString(link), html.EscapeString(inviter))
	return subject, text, htmlBody
}

// Signup confirms a new owner's email (P1-084, P1-086, BR-026, BR-128), in
// English (BR-016): the 6-digit code for the screen they are on, and the link
// for anyone who closed it.
func Signup(shop, name, code, link string) (subject, text, htmlBody string) {
	subject = code + " is your Belli.id code"
	text = fmt.Sprintf(`Hi %s,

Your code to open %s on Belli.id:

    %s

It works for 30 minutes. Or confirm your email and choose a password with this link, which works for 7 days:
%s

If both have expired, register again with the same email to get new ones.
If you did not sign up, you can ignore this email.
`, name, shop, code, link)
	htmlBody = fmt.Sprintf(`<p>Hi %s,</p>
<p>Your code to open <strong>%s</strong> on Belli.id:</p>
<p style="font-size:28px;font-weight:700;letter-spacing:6px">%s</p>
<p>It works for 30 minutes. Or <a href="%s">confirm your email and choose a password</a> with this link, which works for 7 days.</p>
<p>If both have expired, register again with the same email to get new ones.
If you did not sign up, you can ignore this email.</p>`,
		html.EscapeString(name), html.EscapeString(shop), html.EscapeString(code), html.EscapeString(link))
	return subject, text, htmlBody
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
