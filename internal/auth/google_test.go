package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// FakeGoogle serves a JWKS for one RSA key and signs ID tokens with it, so a
// test can be Google.
type FakeGoogle struct {
	Server *httptest.Server
	key    *rsa.PrivateKey
	Hits   int
}

func NewFakeGoogle(t *testing.T) *FakeGoogle {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &FakeGoogle{key: key}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.Hits++
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "k1", "kty": "RSA", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(f.Server.Close)
	return f
}

// Token signs claims as Google would, with kid k1.
func (f *FakeGoogle) Token(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = "k1"
	s, err := tok.SignedString(f.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func googleClaimsFor(aud, sub, email string) jwt.MapClaims {
	return jwt.MapClaims{"iss": "https://accounts.google.com", "aud": aud, "sub": sub, "email": email,
		"email_verified": true, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
}

// TestGoogleVerifier is BR-127's checks: signature, issuer, audience, expiry
// and a verified email, each refused on its own.
func TestGoogleVerifier(t *testing.T) {
	g := NewFakeGoogle(t)
	v := &GoogleVerifier{CertsURL: g.Server.URL, HTTP: g.Server.Client()}
	const client = "shop-client.apps.googleusercontent.com"

	id, err := v.Verify(t.Context(), g.Token(t, googleClaimsFor(client, "g-123", "rina@example.com")), client)
	if err != nil || id.Subject != "g-123" || id.Email != "rina@example.com" {
		t.Fatalf("valid: %+v %v", id, err)
	}
	stringVerified := googleClaimsFor(client, "g-1", "a@example.com")
	stringVerified["email_verified"] = "true"
	if _, err := v.Verify(t.Context(), g.Token(t, stringVerified), client); err != nil {
		t.Errorf(`email_verified "true": %v`, err)
	}

	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	forged := jwt.NewWithClaims(jwt.SigningMethodRS256, googleClaimsFor(client, "g-1", "a@example.com"))
	forged.Header["kid"] = "k1"
	forgedRaw, _ := forged.SignedString(other)
	pub, _ := x509.MarshalPKIXPublicKey(&g.key.PublicKey)
	hs := jwt.NewWithClaims(jwt.SigningMethodHS256, googleClaimsFor(client, "g-1", "a@example.com"))
	hs.Header["kid"] = "k1"
	hsRaw, _ := hs.SignedString(pub)
	noKid := jwt.NewWithClaims(jwt.SigningMethodRS256, googleClaimsFor(client, "g-1", "a@example.com"))
	noKidRaw, _ := noKid.SignedString(g.key)

	edit := func(k string, v any) string {
		c := googleClaimsFor(client, "g-1", "a@example.com")
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
		return g.Token(t, c)
	}
	for what, raw := range map[string]string{
		"another app's audience":    g.Token(t, googleClaimsFor("someone-else", "g-1", "a@example.com")),
		"another key's signature":   forgedRaw,
		"HS256 with the public key": hsRaw,
		"no kid":                    noKidRaw,
		"an unverified email":       edit("email_verified", false),
		"another issuer":            edit("iss", "https://evil.example"),
		"expired":                   edit("exp", time.Now().Add(-time.Hour).Unix()),
		"no expiry":                 edit("exp", nil),
		"no email":                  edit("email", nil),
		"garbage":                   "not.a.jwt",
	} {
		if _, err := v.Verify(t.Context(), raw, client); !errors.Is(err, ErrInvalidGoogleToken) {
			t.Errorf("%s: %v, want ErrInvalidGoogleToken", what, err)
		}
	}
	if g.Hits != 1 {
		t.Errorf("fetched Google's keys %d times; they are cached for their max-age", g.Hits)
	}
}
