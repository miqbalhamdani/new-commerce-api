-- Brands (P1-021, 04-api-spec.md §6.1). Every query runs inside InTenantTx,
-- so RLS has already narrowed it to one tenant.

-- name: Slugify :one
SELECT slugify(sqlc.arg(txt)::text)::text;

-- name: CreateBrand :one
INSERT INTO brands (id, tenant_id, name, slug)
VALUES (sqlc.arg(id), current_setting('app.tenant_id')::uuid, sqlc.arg(name), slugify(sqlc.arg(name)))
RETURNING *;

-- name: GetBrand :one
SELECT * FROM brands WHERE id = $1;

-- name: RenameBrand :one
UPDATE brands SET name = sqlc.arg(name), slug = slugify(sqlc.arg(name)), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: ArchiveBrand :one
-- Archiving twice keeps the first timestamp.
UPDATE brands SET archived_at = coalesce(archived_at, now()), updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ListBrands :many
-- Keyset over (name, id). q is a substring match on the name; the caller
-- escapes LIKE metacharacters.
SELECT * FROM brands
WHERE (archived_at IS NOT NULL) = sqlc.arg(archived)::bool
  AND (sqlc.narg(q)::text IS NULL OR name ILIKE '%' || sqlc.narg(q)::text || '%')
  AND (sqlc.narg(after_name)::text IS NULL
       OR (name, id) > (sqlc.narg(after_name)::text, sqlc.narg(after_id)::uuid))
ORDER BY name, id
LIMIT sqlc.arg(lim);
