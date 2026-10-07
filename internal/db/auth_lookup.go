package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The three reads in this file are the only ones in the system that cross a
// tenant boundary (BR-003), and they are hand-written rather than generated so
// that they are visible as such -- generated queries all look alike, and these
// must not blend in.
//
// Each goes through a SECURITY DEFINER function owned by a role with BYPASSRLS
// (migrations 000003 and 000006). The function fixes the returned columns at
// definition time; widening one is a schema change, a contract change, and a
// security review. Nothing else may read across tenants from a request path.
//
// They exist because every caller runs before a tenant is known. Login has
// only an email; refresh has only a cookie; a storefront request has only an
// API key. A plain query at that point matches zero rows, because FORCE RLS
// compares tenant_id against a NULL setting.

// ErrNotFound is returned when a lookup matches nothing. Callers must not
// distinguish it from a wrong password in anything they send to a client.
var ErrNotFound = errors.New("not found")

// AuthUser is what the login path is allowed to learn about a user before it
// has authenticated them. Note what is absent: name, email, created_at.
type AuthUser struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	PasswordHash *string // nil while an invitation is outstanding
	Status       string
	Role         string
}

// LookupUserForAuth resolves an email to the one user that owns it.
//
// Email is unique across the whole system (BR-020), which is what makes
// "one user" true and lets login carry no tenant parameter.
func (s *Store) LookupUserForAuth(ctx context.Context, email string) (AuthUser, error) {
	var u AuthUser
	err := s.pool.QueryRow(ctx,
		`SELECT id, tenant_id, password_hash, status, role FROM auth_lookup_user($1)`,
		email,
	).Scan(&u.ID, &u.TenantID, &u.PasswordHash, &u.Status, &u.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthUser{}, ErrNotFound
	}
	if err != nil {
		return AuthUser{}, fmt.Errorf("look up user for auth: %w", err)
	}
	return u, nil
}

// AuthRefreshToken is what the refresh path learns before it has a tenant.
// Deliberately carries no token material: the caller already holds the token,
// and the stored hash would be a verifier if it leaked.
type AuthRefreshToken struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	UserID    uuid.UUID
	ExpiresAt time.Time
	RevokedAt *time.Time
}

// LookupRefreshToken resolves a token hash to its tenant, so the rotation that
// follows can run inside InTenantTx like any other write.
func (s *Store) LookupRefreshToken(ctx context.Context, tokenHash string) (AuthRefreshToken, error) {
	var t AuthRefreshToken
	err := s.pool.QueryRow(ctx,
		`SELECT id, tenant_id, user_id, expires_at, revoked_at FROM auth_lookup_refresh_token($1)`,
		tokenHash,
	).Scan(&t.ID, &t.TenantID, &t.UserID, &t.ExpiresAt, &t.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthRefreshToken{}, ErrNotFound
	}
	if err != nil {
		return AuthRefreshToken{}, fmt.Errorf("look up refresh token: %w", err)
	}
	return t, nil
}

// APIKey is what the storefront middleware learns from a key before it has a
// tenant (BR-003): no name, no hash, nothing a caller could list keys with.
type APIKey struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	AllowedOrigin string
	RevokedAt     *time.Time
}

// ResolveAPIKey resolves a key's SHA-256 hash to its tenant and allowed origin.
// A revoked key still resolves; refusing it is the caller's decision, so the
// answer can be cached (BR-085).
func (s *Store) ResolveAPIKey(ctx context.Context, keyHash string) (APIKey, error) {
	var k APIKey
	err := s.pool.QueryRow(ctx,
		`SELECT id, tenant_id, allowed_origin, revoked_at FROM resolve_api_key($1)`,
		keyHash,
	).Scan(&k.ID, &k.TenantID, &k.AllowedOrigin, &k.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return APIKey{}, ErrNotFound
	}
	if err != nil {
		return APIKey{}, fmt.Errorf("resolve api key: %w", err)
	}
	return k, nil
}
