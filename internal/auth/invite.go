package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// InviteTTL is how long an invitation link works (BR-026).
const InviteTTL = 7 * 24 * time.Hour

// ErrInvalidInvite covers a token that is malformed, forged or expired. The
// client hears one thing: ask for a new invitation.
var ErrInvalidInvite = errors.New("invalid invitation")

// Invite is what an invitation token proves: which user, at which tenant,
// until when. The token is signed and never stored; it stops working once the
// user is active, because accepting requires status invited (BR-026).
type Invite struct {
	UserID   uuid.UUID `json:"u"`
	TenantID uuid.UUID `json:"t"`
	Expires  int64     `json:"e"`
}

// InviteSigner mints and checks invitation tokens.
type InviteSigner struct{ key []byte }

func NewInviteSigner(secret string) (*InviteSigner, error) {
	if len(secret) < 32 {
		return nil, errors.New("invite secret must be at least 32 bytes")
	}
	return &InviteSigner{key: []byte(secret)}, nil
}

// Mint is "inv_<payload>.<signature>", both base64url.
func (s *InviteSigner) Mint(user, tenantID uuid.UUID, now time.Time) string {
	payload, _ := json.Marshal(Invite{UserID: user, TenantID: tenantID, Expires: now.Add(InviteTTL).Unix()})
	p := base64.RawURLEncoding.EncodeToString(payload)
	return "inv_" + p + "." + base64.RawURLEncoding.EncodeToString(s.sign(p))
}

// Parse checks the signature and the expiry.
func (s *InviteSigner) Parse(token string, now time.Time) (Invite, error) {
	body, ok := strings.CutPrefix(token, "inv_")
	p, sig, ok2 := strings.Cut(body, ".")
	if !ok || !ok2 {
		return Invite{}, ErrInvalidInvite
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, s.sign(p)) {
		return Invite{}, ErrInvalidInvite
	}
	raw, err := base64.RawURLEncoding.DecodeString(p)
	var inv Invite
	if err != nil || json.Unmarshal(raw, &inv) != nil || now.Unix() >= inv.Expires {
		return Invite{}, ErrInvalidInvite
	}
	return inv, nil
}

func (s *InviteSigner) sign(payload string) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte("invite:" + payload))
	return m.Sum(nil)
}

// CodeWindow is one step of a signup code's life. A code is accepted in the
// window it was minted in and the two after it, so it works for at least 30
// minutes (BR-026).
const CodeWindow = 15 * time.Minute

// Code is the 6-digit code a signup email carries next to its link. It is
// never stored: an HMAC over the user, the tenant and the window (BR-026). It
// stops working once the user is active, because the caller checks that.
func (s *InviteSigner) Code(user, tenantID uuid.UUID, now time.Time) string {
	return s.code(user, tenantID, window(now))
}

// CheckCode reports whether code is the user's for this window or one of the
// two before it. Every candidate is compared, so the time taken says nothing.
func (s *InviteSigner) CheckCode(user, tenantID uuid.UUID, code string, now time.Time) bool {
	ok := false
	for back := int64(0); back <= 2; back++ {
		ok = hmac.Equal([]byte(s.code(user, tenantID, window(now)-back)), []byte(code)) || ok
	}
	return ok
}

func window(now time.Time) int64 { return now.Unix() / int64(CodeWindow/time.Second) }

func (s *InviteSigner) code(user, tenantID uuid.UUID, w int64) string {
	m := hmac.New(sha256.New, s.key)
	_, _ = fmt.Fprintf(m, "code:%s:%s:%d", user, tenantID, w) // a hash never fails a write
	return fmt.Sprintf("%06d", binary.BigEndian.Uint32(m.Sum(nil)[:4])%1_000_000)
}
