package config

import "errors"

const devOrderTokenSecret = "insecure-development-only-order-token-key"

// OrderTokenSecret is the key k of a guest's order token (BR-091). BR-091
// places it in a managed KMS; until one exists it is an env secret, like
// INVITE_SECRET, with no default outside development. Rotating it signs
// every guest out of every order link at once, which is the point.
func OrderTokenSecret() (string, error) {
	if v := Getenv("ORDER_TOKEN_SECRET", ""); v != "" {
		return v, nil
	}
	if IsDevelopment() {
		return devOrderTokenSecret, nil
	}
	return "", errors.New("ORDER_TOKEN_SECRET is required when ENVIRONMENT is not \"development\"")
}

const devPasswordResetSecret = "insecure-development-only-reset-key-32"

// PasswordResetSecret signs customer password reset links (BR-094). Like
// INVITE_SECRET, a deployed environment has no default.
func PasswordResetSecret() (string, error) {
	if v := Getenv("PASSWORD_RESET_SECRET", ""); v != "" {
		return v, nil
	}
	if IsDevelopment() {
		return devPasswordResetSecret, nil
	}
	return "", errors.New("PASSWORD_RESET_SECRET is required when ENVIRONMENT is not \"development\"")
}
