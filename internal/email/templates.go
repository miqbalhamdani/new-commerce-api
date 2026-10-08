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
