package email

import (
	"fmt"
	"html"
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
