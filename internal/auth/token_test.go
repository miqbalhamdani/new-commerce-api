package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const testSecret = "test-signing-key-at-least-32-bytes-long"

func TestSigner(t *testing.T) {
	signer, err := NewSigner(testSecret)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	userID, tenantID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	now := time.Now()

	t.Run("round trips the tenant and user", func(t *testing.T) {
		raw, err := signer.Issue(userID, tenantID, "ops", now)
		if err != nil {
			t.Fatalf("issue: %v", err)
		}
		claims, err := signer.Parse(raw)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if claims.TenantID != tenantID {
			t.Errorf("tenant = %s, want %s", claims.TenantID, tenantID)
		}
		if claims.UserID() != userID {
			t.Errorf("user = %s, want %s", claims.UserID(), userID)
		}
		if claims.Role != "ops" {
			t.Errorf("role = %q, want %q", claims.Role, "ops")
		}
	})

	t.Run("rejects a token signed with another key", func(t *testing.T) {
		other, err := NewSigner("a-completely-different-key-32-bytes-x")
		if err != nil {
			t.Fatalf("new signer: %v", err)
		}
		raw, err := other.Issue(userID, tenantID, "ops", now)
		if err != nil {
			t.Fatalf("issue: %v", err)
		}
		if _, err := signer.Parse(raw); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("got %v, want ErrInvalidToken", err)
		}
	})

	t.Run("rejects an expired token", func(t *testing.T) {
		raw, err := signer.Issue(userID, tenantID, "ops", now.Add(-2*AccessTokenTTL))
		if err != nil {
			t.Fatalf("issue: %v", err)
		}
		if _, err := signer.Parse(raw); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("got %v, want ErrInvalidToken", err)
		}
	})

	// The classic JWT forgery: a token whose header claims no signature was
	// needed. A parser that trusts the header accepts anything.
	t.Run("rejects alg=none", func(t *testing.T) {
		unsigned, err := jwt.NewWithClaims(jwt.SigningMethodNone, Claims{
			TenantID: tenantID,
			RegisteredClaims: jwt.RegisteredClaims{
				Subject:   userID.String(),
				ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
			},
		}).SignedString(jwt.UnsafeAllowNoneSignatureType)
		if err != nil {
			t.Fatalf("build unsigned token: %v", err)
		}
		if _, err := signer.Parse(unsigned); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("got %v, want ErrInvalidToken -- an unsigned token was accepted", err)
		}
	})

	// HS256's security is the secret. A short one is brute-forceable offline
	// from a single captured token.
	t.Run("refuses a short secret at construction", func(t *testing.T) {
		if _, err := NewSigner("too-short"); err == nil {
			t.Error("NewSigner accepted a 9-byte secret")
		}
	})
}

func TestRefreshToken(t *testing.T) {
	t.Run("hashes deterministically and uniquely", func(t *testing.T) {
		token, hash, err := NewRefreshToken()
		if err != nil {
			t.Fatalf("new: %v", err)
		}
		if HashRefreshToken(token) != hash {
			t.Error("hashing the returned token does not reproduce the returned hash")
		}
		if strings.Contains(hash, token) {
			t.Error("the hash contains the token itself")
		}

		other, _, err := NewRefreshToken()
		if err != nil {
			t.Fatalf("new: %v", err)
		}
		if other == token {
			t.Error("two refresh tokens are identical; they are not random")
		}
	})
}

// TestAudiences is BR-086: staff tokens are refused on storefront routes and
// customer tokens on admin routes, and a token for neither is refused by both.
// One key signs both kinds, so the audience is all that separates them.
func TestAudiences(t *testing.T) {
	signer, err := NewSigner("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	staff, _ := signer.Issue(uuid.New(), uuid.New(), RoleOwner, now)
	customer, _ := signer.IssueCustomer(uuid.New(), uuid.New(), now)
	noAudience, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{TenantID: uuid.New(),
		RegisteredClaims: jwt.RegisteredClaims{Subject: uuid.NewString(),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute))}}).SignedString(signer.secret)

	if _, err := signer.Parse(staff); err != nil {
		t.Errorf("admin refuses a staff token: %v", err)
	}
	if c, err := signer.ParseCustomer(customer); err != nil || c.Role != "" {
		t.Errorf("storefront refuses a customer token: %v %+v", err, c)
	}
	for what, err := range map[string]error{
		"a customer token on admin":        second(signer.Parse(customer)),
		"a staff token on the storefront":  second(signer.ParseCustomer(staff)),
		"a token with no audience, admin":  second(signer.Parse(noAudience)),
		"one with no audience, storefront": second(signer.ParseCustomer(noAudience)),
	} {
		if err == nil {
			t.Errorf("%s was accepted", what)
		}
	}
}

func second[T any](_ T, err error) error { return err }
