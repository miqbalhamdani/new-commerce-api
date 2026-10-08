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
