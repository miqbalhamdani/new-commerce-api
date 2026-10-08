-- Variants (P1-029, 04-api-spec.md §7.2). price is variant_price() -- the one
-- place a price is decided (BR-046); nothing reads the amounts to charge.

-- name: CreateVariant :one
INSERT INTO variants (id, tenant_id, product_id, sku, barcode, option_values,
                      regular_price_amount, sale_price_amount, sale_starts_at, sale_ends_at, weight_grams)
VALUES (sqlc.arg(id), current_setting('app.tenant_id')::uuid, sqlc.arg(product_id), sqlc.narg(sku), sqlc.narg(barcode),
        sqlc.arg(option_values), sqlc.arg(regular_price), sqlc.narg(sale_price),
        sqlc.narg(sale_starts_at), sqlc.narg(sale_ends_at), sqlc.arg(weight_grams))
RETURNING id;

-- name: GetVariant :one
SELECT v.*, variant_price(v)::bigint AS price FROM variants v WHERE v.id = $1;

-- name: ListVariants :many
-- Creation order: v7 ids are time-ordered, and the matrix creates rows in grid order.
SELECT v.*, variant_price(v)::bigint AS price FROM variants v
WHERE v.product_id = $1 AND (v.archived_at IS NOT NULL) = sqlc.arg(archived)::bool
ORDER BY v.id;

-- name: UpdateVariant :one
UPDATE variants SET
  sku                  = CASE WHEN sqlc.arg(set_sku)::bool THEN sqlc.narg(sku) ELSE sku END,
  barcode              = CASE WHEN sqlc.arg(set_barcode)::bool THEN sqlc.narg(barcode) ELSE barcode END,
  regular_price_amount = coalesce(sqlc.narg(regular_price), regular_price_amount),
  sale_price_amount    = CASE WHEN sqlc.arg(set_sale_price)::bool THEN sqlc.narg(sale_price) ELSE sale_price_amount END,
  sale_starts_at       = CASE WHEN sqlc.arg(set_sale_starts)::bool THEN sqlc.narg(sale_starts_at) ELSE sale_starts_at END,
  sale_ends_at         = CASE WHEN sqlc.arg(set_sale_ends)::bool THEN sqlc.narg(sale_ends_at) ELSE sale_ends_at END,
  weight_grams         = coalesce(sqlc.narg(weight_grams), weight_grams),
  version              = version + 1,
  updated_at           = now()
WHERE id = sqlc.arg(id) AND version = sqlc.arg(version)
RETURNING id;

-- name: ArchiveVariant :one
UPDATE variants SET archived_at = coalesce(archived_at, now()), version = version + 1, updated_at = now()
WHERE id = $1
RETURNING id;

-- name: SKUHolder :one
-- The variant and product that already hold a SKU, for the duplicate_sku
-- message (BR-039).
SELECT v.id, p.title FROM variants v JOIN products p ON p.id = v.product_id WHERE v.sku = sqlc.arg(sku)::text;

-- name: AllVariants :many
-- Live and archived, for the matrix diff.
SELECT v.*, variant_price(v)::bigint AS price FROM variants v WHERE v.product_id = $1 ORDER BY v.id;

-- name: RestoreVariant :exec
UPDATE variants SET archived_at = NULL, version = version + 1, updated_at = now() WHERE id = $1;
