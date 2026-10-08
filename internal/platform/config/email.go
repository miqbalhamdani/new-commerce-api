package config

import "errors"

// ResendAPIKey sends email through Resend (BR-128). Empty means log-only:
// the worker logs who would have been emailed, never the body.
func ResendAPIKey() string { return Getenv("RESEND_API_KEY", "") }

// EmailDomain is what no-reply@ is sent from; the real one arrives with P1-225.
func EmailDomain() string { return Getenv("EMAIL_DOMAIN", "example.com") }

// AdminURL is where links in email point: the admin app.
func AdminURL() string { return Getenv("ADMIN_URL", "http://localhost:3000") }

const devInviteSecret = "insecure-development-only-invite-key-32"

// InviteSecret signs invitation tokens (BR-026). Like JWTSecret, a deployed
// environment has no default.
func InviteSecret() (string, error) {
	if v := Getenv("INVITE_SECRET", ""); v != "" {
		return v, nil
	}
	if IsDevelopment() {
		return devInviteSecret, nil
	}
	return "", errors.New("INVITE_SECRET is required when ENVIRONMENT is not \"development\"")
}
