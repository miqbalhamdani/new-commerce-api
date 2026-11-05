package auth

import "strings"

// PlatformAdmins is PLATFORM_ADMIN_EMAILS as a set (BR-150): the staff who may
// open the platform admin report. It is not a role and grants nothing in the
// user's own shop. The zero value lets no one.
type PlatformAdmins map[string]struct{}

// NewPlatformAdmins builds the set from config.PlatformAdminEmails.
func NewPlatformAdmins(emails []string) PlatformAdmins {
	p := make(PlatformAdmins, len(emails))
	for _, e := range emails {
		p[strings.ToLower(strings.TrimSpace(e))] = struct{}{}
	}
	return p
}

// Has reports whether email is a platform admin's, in any case.
func (p PlatformAdmins) Has(email string) bool {
	_, ok := p[strings.ToLower(strings.TrimSpace(email))]
	return ok && email != ""
}
