package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ResetTTL is how long a password reset link works (BR-094).
const ResetTTL = 30 * time.Minute

// ErrInvalidReset covers a reset token that is malformed, forged, expired or
// already used. The customer or staff user hears one thing: ask for a new
// link.
var ErrInvalidReset = errors.New("invalid password reset")

// Reset is what a reset token names: which customer or staff user, of which
// shop, until when.
type Reset struct {
	ID       uuid.UUID `json:"c"`
	TenantID uuid.UUID `json:"t"`
	Expires  int64     `json:"e"`
}

// ResetTokens mint and check password reset tokens. Nothing is stored
// (03-erd.md §3.5): the account's password hash at the time of minting is
// part of what is signed, so setting a password -- with this link or any
// other way -- changes the hash and spends every link already sent. A
// Google-only account's missing hash counts as empty.
type ResetTokens struct {
	key   []byte
	label string
}

// NewResetTokens is the customers' tokens; Staff is the admin's.
func NewResetTokens(secret string) (*ResetTokens, error) {
	if len(secret) < 32 {
		return nil, errors.New("password reset secret must be at least 32 bytes")
	}
	return &ResetTokens{key: []byte(secret), label: "reset:"}, nil
}

// Staff is the same key signing under another label, so a customer's link
// never resets a staff password, nor a staff link a customer's (BR-094).
func (s *ResetTokens) Staff() *ResetTokens {
	return &ResetTokens{key: s.key, label: "staff-reset:"}
}

// Mint is "prt_<payload>.<signature>", both base64url.
func (s *ResetTokens) Mint(id, tenantID uuid.UUID, passwordHash *string, now time.Time) string {
	payload, _ := json.Marshal(Reset{ID: id, TenantID: tenantID, Expires: now.Add(ResetTTL).Unix()})
	p := base64.RawURLEncoding.EncodeToString(payload)
	return "prt_" + p + "." + base64.RawURLEncoding.EncodeToString(s.sign(p, passwordHash))
}

// Parse reads which account a token names and checks it has not expired.
// The signature waits for Verify, which needs that customer's current hash.
func (s *ResetTokens) Parse(token string, now time.Time) (Reset, error) {
	p, _, ok := split(token)
	if !ok {
		return Reset{}, ErrInvalidReset
	}
	raw, err := base64.RawURLEncoding.DecodeString(p)
	var r Reset
	if err != nil || json.Unmarshal(raw, &r) != nil || now.Unix() >= r.Expires {
		return Reset{}, ErrInvalidReset
	}
	return r, nil
}

// Verify is whether token was minted against passwordHash, in constant time.
func (s *ResetTokens) Verify(token string, passwordHash *string) bool {
	p, sig, ok := split(token)
	if !ok {
		return false
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	return err == nil && hmac.Equal(got, s.sign(p, passwordHash))
}

func split(token string) (payload, sig string, ok bool) {
	body, ok1 := strings.CutPrefix(token, "prt_")
	payload, sig, ok2 := strings.Cut(body, ".")
	return payload, sig, ok1 && ok2
}

func (s *ResetTokens) sign(payload string, passwordHash *string) []byte {
	hash := ""
	if passwordHash != nil {
		hash = *passwordHash
	}
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(s.label + payload + "\x00" + hash))
	return m.Sum(nil)
}
