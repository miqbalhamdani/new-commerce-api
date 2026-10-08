-- Product media (P1-043, 04-api-spec.md §8). Rows hold object keys, never
-- URLs (BR-050).

-- name: CreateMedia :one
-- Appended after the product's other images. Confirming the same key twice is
-- the same image: the conflict returns nothing and the caller reads the row.
INSERT INTO product_media (id, tenant_id, product_id, variant_id, r2_key, mime_type, bytes, position)
VALUES (sqlc.arg(id), current_setting('app.tenant_id')::uuid, sqlc.arg(product_id), sqlc.narg(variant_id),
        sqlc.arg(r2_key), sqlc.arg(mime_type), sqlc.arg(bytes),
        (SELECT coalesce(max(position) + 1, 0) FROM product_media WHERE product_id = sqlc.arg(product_id)))
ON CONFLICT (product_id, r2_key) DO NOTHING
RETURNING *;

-- name: GetMediaByKey :one
SELECT * FROM product_media WHERE product_id = $1 AND r2_key = $2;

-- name: GetMedia :one
SELECT * FROM product_media WHERE id = $1;

-- name: ListProductMedia :many
SELECT * FROM product_media WHERE product_id = $1 ORDER BY position, id;

-- name: SetMediaVariant :one
UPDATE product_media SET variant_id = sqlc.narg(variant_id) WHERE id = sqlc.arg(id) RETURNING *;

-- name: SetMediaPosition :exec
UPDATE product_media SET position = sqlc.arg(position) WHERE id = sqlc.arg(id);

-- name: DeleteMedia :one
DELETE FROM product_media WHERE id = $1 RETURNING *;

-- name: MediaKeyInUse :one
SELECT EXISTS (SELECT 1 FROM product_media WHERE r2_key = $1);

-- name: SetMediaDerivatives :execrows
-- The worker's result for one image (P1-044).
UPDATE product_media SET derivatives = sqlc.arg(derivatives), width = sqlc.arg(width), height = sqlc.arg(height)
WHERE id = sqlc.arg(id);

-- name: LiveVariantOfProduct :one
SELECT EXISTS (SELECT 1 FROM variants WHERE id = sqlc.arg(id) AND product_id = sqlc.arg(product_id) AND archived_at IS NULL);
