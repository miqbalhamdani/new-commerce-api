// Package secrets seals the small credentials the API keeps for a shop -- a
// Midtrans server key (P1-216), later channel OAuth tokens (P1-301) -- before
// they are stored (BR-101, BR-129).
//
// Envelope encryption: every value gets its own random data key, the value is
// sealed with it (AES-256-GCM), and the data key is sealed with the key
// encryption key. The KEK is an env secret until there is a managed KMS
// (05-backlog.md, Phase 3); moving it there replaces wrap and unwrap and
// nothing else, and the key id in every value says which KEK sealed it.
//
// The shop's id is GCM additional data on both layers, so a value copied into
// another shop's row does not open.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
)

// ErrCannotOpen is every way a value can fail to open: tampered, truncated,
// sealed by another key or for another shop. Callers cannot tell them apart.
var ErrCannotOpen = errors.New("secrets: cannot open")

const (
	version = 1
	keySize = 32 // AES-256
)

// Box seals and opens values with one key encryption key.
type Box struct {
	kek cipher.AEAD
	kid string
}

// New is a Box over a 32-byte KEK. Its id is a hash prefix: enough to say
// which key sealed a value, nothing about the key.
func New(kek []byte) (*Box, error) {
	if len(kek) != keySize {
		return nil, fmt.Errorf("secrets: key encryption key must be %d bytes, got %d", keySize, len(kek))
	}
	aead, err := gcm(kek)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(kek)
	return &Box{kek: aead, kid: hex.EncodeToString(sum[:4])}, nil
}

// FromEnv is the Box over ENCRYPTION_KEY.
func FromEnv() (*Box, error) {
	raw, err := config.EncryptionKey()
	if err != nil {
		return nil, err
	}
	kek, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("secrets: ENCRYPTION_KEY is not base64: %w", err)
	}
	return New(kek)
}

// Seal encrypts plain for one shop. The layout is
//
//	version | len(kid) | kid | KEK nonce | wrapped data key | data nonce | ciphertext
//
// and two seals of one value never look alike.
func (b *Box) Seal(tenantID uuid.UUID, plain []byte) ([]byte, error) {
	dek := make([]byte, keySize)
	if _, err := rand.Read(dek); err != nil {
		return nil, err
	}
	data, err := gcm(dek)
	if err != nil {
		return nil, err
	}
	out := append([]byte{version, byte(len(b.kid))}, b.kid...)
	out, err = seal(b.kek, out, dek, tenantID[:])
	if err != nil {
		return nil, err
	}
	return seal(data, out, plain, tenantID[:])
}

// Open decrypts a value Seal made for the same shop.
func (b *Box) Open(tenantID uuid.UUID, sealed []byte) ([]byte, error) {
	if len(sealed) < 2 || sealed[0] != version {
		return nil, ErrCannotOpen
	}
	n := int(sealed[1])
	if len(sealed) < 2+n || string(sealed[2:2+n]) != b.kid {
		return nil, ErrCannotOpen
	}
	rest := sealed[2+n:]
	wrapped := b.kek.NonceSize() + keySize + b.kek.Overhead()
	if len(rest) < wrapped {
		return nil, ErrCannotOpen
	}
	dek, err := open(b.kek, rest[:wrapped], tenantID[:])
	if err != nil {
		return nil, ErrCannotOpen
	}
	data, err := gcm(dek)
	if err != nil {
		return nil, ErrCannotOpen
	}
	plain, err := open(data, rest[wrapped:], tenantID[:])
	if err != nil {
		return nil, ErrCannotOpen
	}
	return plain, nil
}

func gcm(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// seal appends a fresh nonce and the sealed plaintext to dst.
func seal(a cipher.AEAD, dst, plain, ad []byte) ([]byte, error) {
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	dst = append(dst, nonce...)
	return a.Seal(dst, nonce, plain, ad), nil
}

func open(a cipher.AEAD, sealed, ad []byte) ([]byte, error) {
	if len(sealed) < a.NonceSize() {
		return nil, ErrCannotOpen
	}
	return a.Open(nil, sealed[:a.NonceSize()], sealed[a.NonceSize():], ad)
}
