package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// TenantRow is one shop as tenants holds it, for the platform admin report
// (BR-150).
type TenantRow struct {
	ID        uuid.UUID
	Name      string
	Slug      string
	Status    string
	Plan      string     // BR-160
	PlanUntil *time.Time // a date; display only
	CreatedAt time.Time
}

// tenantColumns are TenantRow's, in its order.
const tenantColumns = "id, name, slug, status, plan, plan_until, created_at"

// Tenants is every shop, newest first, for BR-150's report. tenants has no
// RLS; everything else the report reads runs in each shop's own InTenantTx.
func (s *Store) Tenants(ctx context.Context) ([]TenantRow, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT "+tenantColumns+" FROM tenants ORDER BY created_at DESC, id DESC")
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[TenantRow])
}

// Tenant is one shop by id, for a platform action (BR-151); ErrNotFound when
// there is none.
func (s *Store) Tenant(ctx context.Context, id uuid.UUID) (TenantRow, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+tenantColumns+" FROM tenants WHERE id = $1", id)
	if err != nil {
		return TenantRow{}, fmt.Errorf("get tenant: %w", err)
	}
	t, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[TenantRow])
	if errors.Is(err, pgx.ErrNoRows) {
		return TenantRow{}, ErrNotFound
	}
	return t, err
}

// ShopStats is one shop's counts and first/last dates for BR-150. A nil time
// means none yet.
type ShopStats struct {
	ProductsActive, ProductsTotal int64
	OrdersTotal, Orders30d        int64
	FirstProductAt                *time.Time
	FirstOrderAt, LastOrderAt     *time.Time
	LastAuditAt, LastLoginAt      *time.Time
	// Registered is whether the shop has its tenant.register audit row, and
	// RegisterVia that row's "via" ("google", or nil for the signup form).
	Registered  bool
	RegisterVia *string
}

// PlatformShopStats reads ShopStats inside the shop's InTenantTx, so RLS
// scopes every table to it. Hand-written rather than in db/queries: sqlc
// types a LATERAL column from a NOT NULL column as non-null, and here NULL is
// the answer "none yet".
func PlatformShopStats(ctx context.Context, tx pgx.Tx) (ShopStats, error) {
	var st ShopStats
	err := tx.QueryRow(ctx, `
SELECT (SELECT count(*) FROM products WHERE archived_at IS NULL AND status = 'active'),
       (SELECT count(*) FROM products WHERE archived_at IS NULL),
       (SELECT count(*) FROM orders),
       (SELECT count(*) FROM orders WHERE placed_at >= now() - interval '30 days'),
       (SELECT min(created_at) FROM products),
       (SELECT min(placed_at) FROM orders),
       (SELECT max(placed_at) FROM orders),
       (SELECT max(created_at) FROM audit_log),
       (SELECT max(last_login_at) FROM users),
       reg.id IS NOT NULL,
       reg.via
  FROM (SELECT 1) AS one
  LEFT JOIN LATERAL (SELECT id, after->>'via' AS via FROM audit_log
                      WHERE action = 'tenant.register' ORDER BY id LIMIT 1) reg ON true`).Scan(
		&st.ProductsActive, &st.ProductsTotal, &st.OrdersTotal, &st.Orders30d,
		&st.FirstProductAt, &st.FirstOrderAt, &st.LastOrderAt, &st.LastAuditAt, &st.LastLoginAt,
		&st.Registered, &st.RegisterVia)
	if err != nil {
		return ShopStats{}, fmt.Errorf("platform shop stats: %w", err)
	}
	return st, nil
}
