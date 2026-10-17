package team

import (
	"regexp"
	"testing"
)

func TestSlugAndPrefix(t *testing.T) {
	for _, c := range []struct{ name, slug, prefix string }{
		{"Erigo Store", "erigo-store", "ERI"},
		{"  Toko   ABC!! ", "toko-abc", "TOK"},
		{"9 Naga", "9-naga", "9NA"},
		{"Kopi", "kopi", "KOP"},
		{"!!", "shop", "SHOP"},
		{"Ö", "shop", "SHOP"},
	} {
		if got := slugFor(c.name); !regexp.MustCompile(`^` + c.slug + `-[0-9a-f]{6}$`).MatchString(got) {
			t.Errorf("slugFor(%q) = %q, want %s-xxxxxx", c.name, got, c.slug)
		}
		if got := prefixFor(c.name); got != c.prefix || !regexp.MustCompile(`^[A-Z0-9]{2,6}$`).MatchString(got) {
			t.Errorf("prefixFor(%q) = %q, want %q", c.name, got, c.prefix)
		}
	}
	for address, ok := range map[string]bool{
		"budi@erigo.co.id": true, "Budi <budi@erigo.co.id>": false, "budi": false, "": false,
	} {
		if err := validSignup("Shop", "Budi", address); (err == nil) != ok {
			t.Errorf("validSignup(%q) = %v", address, err)
		}
	}
}
