-- Products (P1-028, 04-api-spec.md §7.1).

-- name: ProductSlugTaken :one
-- Archived products keep their slug (BR-042), so they count.
SELECT EXISTS (SELECT 1 FROM products WHERE slug = $1 AND id IS DISTINCT FROM sqlc.narg(except_id)::uuid);

-- name: CreateProduct :one
INSERT INTO products (id, tenant_id, title, slug, description, brand_id, attributes)
VALUES (sqlc.arg(id), current_setting('app.tenant_id')::uuid, sqlc.arg(title), sqlc.arg(slug),
        sqlc.narg(description), sqlc.narg(brand_id), sqlc.arg(attributes))
RETURNING *;

-- name: GetProduct :one
SELECT * FROM products WHERE id = $1;

-- name: UpdateProduct :one
-- Optimistic concurrency (BR-010): zero rows means a stale version or no row.
UPDATE products SET
  title       = coalesce(sqlc.narg(title), title),
  slug        = coalesce(sqlc.narg(slug), slug),
  description = CASE WHEN sqlc.arg(set_description)::bool THEN sqlc.narg(description) ELSE description END,
  brand_id    = CASE WHEN sqlc.arg(set_brand)::bool THEN sqlc.narg(brand_id)::uuid ELSE brand_id END,
  attributes  = coalesce(sqlc.narg(attributes), attributes),
  status      = coalesce(sqlc.narg(status), status),
  version     = version + 1,
  updated_at  = now()
WHERE id = sqlc.arg(id) AND version = sqlc.arg(version)
RETURNING *;

-- name: ArchiveProduct :one
UPDATE products SET status = 'archived', archived_at = coalesce(archived_at, now()),
       version = version + 1, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: LiveCategoryCount :one
SELECT count(*)::int FROM categories WHERE id = ANY(sqlc.arg(ids)::uuid[]) AND archived_at IS NULL;

-- name: ClearProductCategories :exec
DELETE FROM product_categories WHERE product_id = $1;

-- name: AddProductCategories :exec
INSERT INTO product_categories (tenant_id, product_id, category_id)
SELECT current_setting('app.tenant_id')::uuid, sqlc.arg(product_id), unnest(sqlc.arg(category_ids)::uuid[]);

-- name: ProductCategories :many
SELECT c.id, c.kind, c.name, c.path FROM product_categories pc
JOIN categories c ON c.id = pc.category_id
WHERE pc.product_id = $1
ORDER BY c.kind, c.path;

-- name: LiveVariantCount :one
SELECT count(*)::int FROM variants WHERE product_id = $1 AND archived_at IS NULL;

-- name: LockProduct :one
-- The matrix holds the product row for its whole transaction, so two grid
-- saves cannot interleave.
SELECT * FROM products WHERE id = $1 FOR UPDATE;

-- name: BumpProduct :one
-- One version step per matrix save, with the option axes it settled on.
UPDATE products SET option_names = sqlc.arg(option_names), version = version + 1, updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING version;
