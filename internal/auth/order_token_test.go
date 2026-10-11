package auth

import (
	"testing"

	"github.com/google/uuid"
)

// TestOrderTokens is BR-091: the same order gets the same token, and a token
// opens nothing else -- not another order, not the same order id in another
// shop, not under another key.
func TestOrderTokens(t *testing.T) {
	tokens, err := NewOrderTokens("a-key-that-is-at-least-32-bytes-long")
	if err != nil {
		t.Fatal(err)
	}
	shop, order := uuid.New(), uuid.New()
	tok := tokens.Mint(shop, order)
	if tok != tokens.Mint(shop, order) || !tokens.Valid(shop, order, tok) {
		t.Fatal("a token does not open its own order")
	}
	other, _ := NewOrderTokens("another-key-that-is-at-least-32-bytes")
	for what, ok := range map[string]bool{
		"another order":     tokens.Valid(shop, uuid.New(), tok),
		"another shop":      tokens.Valid(uuid.New(), order, tok),
		"another key":       other.Valid(shop, order, tok),
		"an empty token":    tokens.Valid(shop, order, ""),
		"a truncated token": tokens.Valid(shop, order, tok[:len(tok)-1]),
	} {
		if ok {
			t.Errorf("the token opens %s", what)
		}
	}
	if _, err := NewOrderTokens("short"); err == nil {
		t.Error("a short secret was accepted")
	}
}
