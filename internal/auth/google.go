package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ErrInvalidGoogleToken is every way a Google ID token can fail: signature,
// issuer, audience, expiry, an unverified email. The shopper hears one 401
// (BR-127).
var ErrInvalidGoogleToken = errors.New("invalid google id token")

// GoogleIdentity is what a verified ID token says: Google's permanent id for
// the person, and an email Google has verified.
type GoogleIdentity struct {
	Subject, Email, Name string
}

// googleIssuers are the two spellings Google signs with.
var googleIssuers = map[string]bool{"accounts.google.com": true, "https://accounts.google.com": true}

// GoogleVerifier checks Google ID tokens against Google's published keys,
// cached for as long as Google says they hold (BR-127). No client secret is
// involved: the token is checked, never exchanged.
type GoogleVerifier struct {
	CertsURL string // https://www.googleapis.com/oauth2/v3/certs; a test serves its own
	HTTP     *http.Client

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	expires   time.Time
	refetched time.Time
}

func NewGoogleVerifier() *GoogleVerifier {
	return &GoogleVerifier{CertsURL: "https://www.googleapis.com/oauth2/v3/certs", HTTP: &http.Client{Timeout: 5 * time.Second}}
}

type googleClaims struct {
	Email         string       `json:"email"`
	Name          string       `json:"name"`
	EmailVerified flexibleBool `json:"email_verified"`
	jwt.RegisteredClaims
}

// flexibleBool is email_verified, which Google has sent as true and as "true".
type flexibleBool bool

func (b *flexibleBool) UnmarshalJSON(raw []byte) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	switch v := v.(type) {
	case bool:
		*b = flexibleBool(v)
	case string:
		*b = v == "true"
	}
	return nil
}

// Verify checks raw for the shop's clientID and returns who it is.
func (g *GoogleVerifier) Verify(ctx context.Context, raw, clientID string) (GoogleIdentity, error) {
	var c googleClaims
	_, err := jwt.ParseWithClaims(raw, &c, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, errors.New("no kid")
		}
		return g.key(ctx, kid)
	},
		// Pinned: an HS256 token "signed" with Google's public key as the
		// secret is the classic forgery.
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithAudience(clientID),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(60*time.Second),
	)
	if err != nil {
		return GoogleIdentity{}, fmt.Errorf("%w: %v", ErrInvalidGoogleToken, err)
	}
	if !googleIssuers[c.Issuer] || c.Subject == "" || c.Email == "" || !bool(c.EmailVerified) {
		return GoogleIdentity{}, ErrInvalidGoogleToken
	}
	return GoogleIdentity{Subject: c.Subject, Email: c.Email, Name: c.Name}, nil
}

// key is Google's public key kid. Keys are cached until Google's max-age;
// an unknown kid -- Google rotated -- refetches, at most once a minute so a
// stream of forged kids cannot make us hammer Google.
func (g *GoogleVerifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if k, ok := g.keys[kid]; ok && time.Now().Before(g.expires) {
		return k, nil
	}
	if time.Since(g.refetched) < time.Minute && time.Now().Before(g.expires) {
		return nil, errors.New("unknown kid")
	}
	g.refetched = time.Now()
	keys, maxAge, err := g.fetch(ctx)
	if err != nil {
		return nil, err
	}
	g.keys, g.expires = keys, time.Now().Add(maxAge)
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, errors.New("unknown kid")
}

var maxAgeRe = regexp.MustCompile(`max-age=(\d+)`)

func (g *GoogleVerifier) fetch(ctx context.Context) (map[string]*rsa.PublicKey, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.CertsURL, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	var set struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil || resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("google certs: status %d: %v", resp.StatusCode, err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if k.Kty != "RSA" || err1 != nil || err2 != nil {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	maxAge := time.Hour
	if m := maxAgeRe.FindStringSubmatch(resp.Header.Get("Cache-Control")); m != nil {
		if secs, err := strconv.Atoi(m[1]); err == nil {
			maxAge = time.Duration(secs) * time.Second
		}
	}
	return keys, maxAge, nil
}
