package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"

	"github.com/google/uuid"
)

// OrderTokens mint and check a guest's order token (BR-091):
// base64url(HMAC-SHA256(k, tenant_id ‖ order_id)). Nothing is stored. A
// replayed checkout recomputes the same token, a lookup recomputes it to
// compare, and a token opens only the one order it was computed for.
type OrderTokens struct{ key []byte }

func NewOrderTokens(secret string) (*OrderTokens, error) {
	if len(secret) < 32 {
		return nil, errors.New("order token secret must be at least 32 bytes")
	}
	return &OrderTokens{key: []byte(secret)}, nil
}

// Mint is the token for one order of one shop.
func (o *OrderTokens) Mint(tenantID, orderID uuid.UUID) string {
	m := hmac.New(sha256.New, o.key)
	m.Write(tenantID[:])
	m.Write(orderID[:])
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Valid reports whether token is that order's, in constant time.
func (o *OrderTokens) Valid(tenantID, orderID uuid.UUID, token string) bool {
	return hmac.Equal([]byte(token), []byte(o.Mint(tenantID, orderID)))
}
