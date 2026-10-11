package db

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// TouchAPIKey records that a key was just used, inside the key's own tenant.
// The storefront calls it when it reads a key from the database rather than its
// cache, so it is one write per key per cache lifetime per process, never one
// per request.
func (s *Store) TouchAPIKey(ctx context.Context, k APIKey) error {
	return s.InTenantTx(tenant.NewContext(ctx, k.TenantID), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE api_keys SET last_used_at = now() WHERE id = $1`, k.ID)
		return err
	})
}
