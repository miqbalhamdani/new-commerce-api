package config

import "errors"

// ResendAPIKey sends email through Resend (BR-128). Empty is development
// only: mail goes to SMTPAddr, or to stdout without one.
func ResendAPIKey() string { return Getenv("RESEND_API_KEY", "") }

// SMTPAddr is a local SMTP server (Mailpit) for development mail. Empty means
// the worker prints mail instead. Ignored once RESEND_API_KEY is set.
func SMTPAddr() string { return Getenv("SMTP_ADDR", "") }

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
