package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestInviteToken(t *testing.T) {
	s, err := NewInviteSigner(strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	user, tenantID, now := uuid.New(), uuid.New(), time.Now()
	tok := s.Mint(user, tenantID, now)
	if !strings.HasPrefix(tok, "inv_") {
		t.Fatalf("token %s", tok)
	}
	inv, err := s.Parse(tok, now.Add(6*24*time.Hour))
	if err != nil || inv.UserID != user || inv.TenantID != tenantID {
		t.Fatalf("parse: %+v %v", inv, err)
	}
	if _, err := s.Parse(tok, now.Add(InviteTTL+time.Second)); err != ErrInvalidInvite {
		t.Errorf("expired token: %v", err)
	}
	other, _ := NewInviteSigner(strings.Repeat("x", 32))
	if _, err := other.Parse(tok, now); err != ErrInvalidInvite {
		t.Errorf("token from another key: %v", err)
	}
	if _, err := s.Parse(tok[:len(tok)-2]+"AA", now); err != ErrInvalidInvite {
		t.Errorf("tampered token: %v", err)
	}
}

// TestSignupCode is BR-026's code: six digits, the same within a window,
// accepted for that window and the two after it, and only for its user.
func TestSignupCode(t *testing.T) {
	s, err := NewInviteSigner(strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	user, tenantID := uuid.New(), uuid.New()
	at := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC) // the start of a window
	code := s.Code(user, tenantID, at)
	if len(code) != 6 || strings.Trim(code, "0123456789") != "" {
		t.Fatalf("code %q is not six digits", code)
	}
	if s.Code(user, tenantID, at.Add(CodeWindow-time.Second)) != code {
		t.Error("the code changed inside its window")
	}
	for _, later := range []time.Duration{0, 29*time.Minute + 59*time.Second, 3*CodeWindow - time.Second} {
		if !s.CheckCode(user, tenantID, code, at.Add(later)) {
			t.Errorf("refused %v after minting", later)
		}
	}
	if s.CheckCode(user, tenantID, code, at.Add(3*CodeWindow)) {
		t.Error("accepted after three windows")
	}
	if s.CheckCode(uuid.New(), tenantID, code, at) || s.CheckCode(user, uuid.New(), code, at) {
		t.Error("accepted for another user or tenant")
	}
	if s.CheckCode(user, tenantID, "", at) {
		t.Error("accepted an empty code")
	}
}
