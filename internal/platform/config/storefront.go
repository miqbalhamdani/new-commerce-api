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

// devEncryptionKey is 32 bytes of zeroes, base64: obviously not a secret.
const devEncryptionKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

// EncryptionKey is the key encryption key that seals shop secrets (BR-101,
// BR-129): 32 random bytes, base64. A managed KMS replaces it later; until
// then a deployed environment has no default. Changing it makes every sealed
// value unreadable, so it is rotated by re-sealing, never by swapping.
func EncryptionKey() (string, error) {
	if v := Getenv("ENCRYPTION_KEY", ""); v != "" {
		return v, nil
	}
	if IsDevelopment() {
		return devEncryptionKey, nil
	}
	return "", errors.New("ENCRYPTION_KEY is required when ENVIRONMENT is not \"development\"")
}

// APIURL is the API's own public base, for URLs other systems call back on:
// the Midtrans notification URL (BR-124). The domain arrives with P1-001.
func APIURL() string { return Getenv("API_URL", "http://localhost:8080") }
