package auth

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestResetTokens is BR-094's token: it works for its customer for 30
// minutes, and stops working once the password changes.
func TestResetTokens(t *testing.T) {
	resets, err := NewResetTokens("a-reset-key-that-is-at-least-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	customer, shop, now := uuid.New(), uuid.New(), time.Now()
	old := "$argon2id$old"
	tok := resets.Mint(customer, shop, &old, now)

	r, err := resets.Parse(tok, now)
	if err != nil || r.ID != customer || r.TenantID != shop || !resets.Verify(tok, &old) {
		t.Fatalf("own token: %+v %v", r, err)
	}
	changed := "$argon2id$new"
	if resets.Verify(tok, &changed) {
		t.Error("a token still verifies after the password changed")
	}
	if _, err := resets.Parse(tok, now.Add(ResetTTL)); err == nil {
		t.Error("a token parses after 30 minutes")
	}
	// A Google-only customer has no hash; their token is bound to that, and
	// spent once they set one.
	google := resets.Mint(customer, shop, nil, now)
	if !resets.Verify(google, nil) || resets.Verify(google, &changed) {
		t.Error("a no-password token does not bind to the missing hash")
	}
	for what, bad := range map[string]string{
		"forged":      tok[:len(tok)-2] + "AA",
		"no prefix":   tok[4:],
		"garbage":     "prt_x.y",
		"empty":       "",
		"other key's": mustResets(t, "another-reset-key-at-least-32-bytes!").Mint(customer, shop, &old, now),
		"staff":       resets.Staff().Mint(customer, shop, &old, now),
	} {
		if resets.Verify(bad, &old) {
			t.Errorf("%s token verifies", what)
		}
	}
	// And the other way: a customer's link never resets a staff password.
	staff := resets.Staff()
	if staff.Verify(tok, &old) || !staff.Verify(staff.Mint(customer, shop, &old, now), &old) {
		t.Error("staff tokens do not stand apart from customers'")
	}
}

func mustResets(t *testing.T, secret string) *ResetTokens {
	t.Helper()
	r, err := NewResetTokens(secret)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
