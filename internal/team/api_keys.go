package team

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// APIKey is a storefront key as lists show it: never the key, never its hash.
type APIKey = sqlcgen.ListApiKeysRow

// apiKeyPrefix marks a storefront key (BR-028), so one pasted into the wrong
// place is recognisable on sight.
const apiKeyPrefix = "sf_live_"

// APIKeys lists the live keys, newest first (P1-200).
func (s *Service) APIKeys(ctx context.Context) ([]APIKey, error) {
	var rows []APIKey
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) (err error) {
		rows, err = q.ListApiKeys(ctx, nil)
		return err
	})
	return rows, err
}

// CreateAPIKey mints a key and returns it with its plaintext, the only time
// the plaintext exists outside the caller's hands: only its SHA-256 is stored
// (BR-028).
func (s *Service) CreateAPIKey(ctx context.Context, name, origin string) (APIKey, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return APIKey{}, "", fieldError("name", "name is required")
	}
	if !ValidOrigin(origin) {
		return APIKey{}, "", originError()
	}
	// 32 random bytes, the same draw as a refresh token, and the same
	// unsalted SHA-256: the input is random, so there is no dictionary.
	random, _, err := auth.NewRefreshToken()
	if err != nil {
		return APIKey{}, "", err
	}
	key := apiKeyPrefix + random
	id := uuid.Must(uuid.NewV7())
	var createdBy *uuid.UUID
	if a, ok := tenant.ActorFromContext(ctx); ok {
		createdBy = &a.UserID
	}
	var k APIKey
	err = s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		if err := q.CreateApiKey(ctx, sqlcgen.CreateApiKeyParams{ID: id, Name: name,
			KeyHash: auth.HashRefreshToken(key), AllowedOrigin: origin, CreatedBy: createdBy}); err != nil {
			return err
		}
		var err error
		if k, err = getAPIKey(ctx, q, id); err != nil {
			return err
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "api_key.create", SubjectType: "api_key", SubjectID: id.String(),
			After: map[string]any{"name": name, "allowed_origin": origin}})
	})
	if err != nil {
		return APIKey{}, "", err
	}
	return k, key, nil
}

// UpdateAPIKey renames a live key or moves it to another origin; nil leaves a
// field alone. A revoked key is 404: it cannot be brought back by editing.
func (s *Service) UpdateAPIKey(ctx context.Context, id uuid.UUID, name, origin *string) (APIKey, error) {
	if name != nil {
		n := strings.TrimSpace(*name)
		if n == "" {
			return APIKey{}, fieldError("name", "name cannot be empty")
		}
		name = &n
	}
	if origin != nil && !ValidOrigin(*origin) {
		return APIKey{}, originError()
	}
	var after APIKey
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		before, err := getAPIKey(ctx, q, id)
		if err != nil {
			return err
		}
		if _, err := q.UpdateApiKey(ctx, sqlcgen.UpdateApiKeyParams{ID: id, Name: name, AllowedOrigin: origin}); err != nil {
			return err
		}
		if after, err = getAPIKey(ctx, q, id); err != nil {
			return err
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "api_key.update", SubjectType: "api_key", SubjectID: id.String(),
			Before: map[string]any{"name": before.Name, "allowed_origin": before.AllowedOrigin},
			After:  map[string]any{"name": after.Name, "allowed_origin": after.AllowedOrigin}})
	})
	return after, err
}

// RevokeAPIKey stops a key for good; keys are revoked, never deleted
// (BR-028). The storefront's resolution cache lets it work for up to 60 s
// more (BR-085).
func (s *Service) RevokeAPIKey(ctx context.Context, id uuid.UUID) error {
	return s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		n, err := q.RevokeApiKey(ctx, id)
		if err != nil {
			return err
		}
		if n == 0 {
			return apperrors.NotFound("No such API key.")
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "api_key.revoke", SubjectType: "api_key", SubjectID: id.String(),
			After: map[string]any{"revoked": true}})
	})
}

func getAPIKey(ctx context.Context, q *sqlcgen.Queries, id uuid.UUID) (APIKey, error) {
	rows, err := q.ListApiKeys(ctx, &id)
	if err != nil {
		return APIKey{}, err
	}
	if len(rows) == 0 {
		return APIKey{}, apperrors.NotFound("No such API key.")
	}
	return rows[0], nil
}

func originError() error {
	return fieldError("allowed_origin",
		"allowed_origin is one exact scheme://host[:port] in lower case, such as https://tokoabc.com: no path, wildcard, trailing slash or default port")
}

// ValidOrigin is whether s is an origin exactly as a browser sends it in the
// Origin header (BR-083), so a stored one can ever match: http or https, a
// lower-case ASCII host, a port only when it is not the scheme's default, and
// nothing else. The column's CHECK is looser (it lets '*', upper case and
// ':443' through), so this is the check that matters.
func ValidOrigin(s string) bool {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Hostname() == "" ||
		s != u.Scheme+"://"+u.Host || strings.HasSuffix(u.Host, ":") {
		return false
	}
	for _, c := range u.Hostname() {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == ':') {
			return false
		}
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 || p != strconv.Itoa(n) ||
			u.Scheme == "https" && n == 443 || u.Scheme == "http" && n == 80 {
			return false
		}
	}
	return true
}
