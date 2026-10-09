-- Categories (P1-024, 04-api-spec.md §6.2). path is maintained by the
-- triggers of migrations 000009/000010; nothing here writes it.

-- name: LockCategories :exec
-- Serialises category writes per tenant, so two crossing moves cannot both
-- pass the cycle check against the other's old tree (BR-034).
SELECT pg_advisory_xact_lock(hashtextextended('categories:' || current_setting('app.tenant_id'), 0));

-- name: CreateCategory :one
INSERT INTO categories (id, tenant_id, parent_id, kind, name, label)
VALUES (sqlc.arg(id), current_setting('app.tenant_id')::uuid, sqlc.narg(parent_id), sqlc.arg(kind), sqlc.arg(name), sqlc.narg(label))
RETURNING *;

-- name: GetCategory :one
SELECT * FROM categories WHERE id = $1;

-- name: UpdateCategory :one
UPDATE categories
   SET name      = CASE WHEN sqlc.arg(set_name)::bool THEN sqlc.arg(name)::text ELSE name END,
       parent_id = CASE WHEN sqlc.arg(set_parent)::bool THEN sqlc.narg(parent_id)::uuid ELSE parent_id END,
       label     = CASE WHEN sqlc.arg(set_label)::bool THEN sqlc.narg(label)::text ELSE label END,
       updated_at = now()
 WHERE id = sqlc.arg(id)
RETURNING *;

-- name: ArchiveCategory :exec
UPDATE categories SET archived_at = coalesce(archived_at, now()), updated_at = now() WHERE id = $1;

-- name: ClearCategoryProducts :execrows
-- Deleting a category takes it off its products, bumping their version so a
-- stale product form gets 409 instead of putting it back (BR-012).
WITH unlinked AS (
  DELETE FROM product_categories WHERE category_id = $1 RETURNING product_id
)
UPDATE products SET version = version + 1, updated_at = now()
 WHERE id IN (SELECT product_id FROM unlinked);

-- name: CategoryCounts :one
-- Live categories below this one, and live products linked anywhere in its
-- subtree -- what the move and delete dialogs state. Live children block a
-- delete (BR-036).
SELECT
  (SELECT count(*) FROM categories d
    WHERE d.kind = c.kind AND d.path <@ c.path AND d.id <> c.id AND d.archived_at IS NULL)::int AS descendant_count,
  (SELECT count(DISTINCT pc.product_id) FROM product_categories pc
     JOIN categories d ON d.id = pc.category_id
     JOIN products p ON p.id = pc.product_id
    WHERE d.kind = c.kind AND d.path <@ c.path AND p.archived_at IS NULL)::int AS product_count
FROM categories c WHERE c.id = $1;

-- name: ListCategories :many
-- Flat, by kind then path. Below parent_id when given; depth counts levels
-- below the parent, or below the roots without one.
SELECT c.* FROM categories c
LEFT JOIN categories base ON base.id = sqlc.narg(parent_id)::uuid
WHERE c.archived_at IS NULL
  AND (sqlc.narg(kind)::text IS NULL OR c.kind = sqlc.narg(kind)::text)
  AND (sqlc.narg(parent_id)::uuid IS NULL
       OR (c.kind = base.kind AND c.path <@ base.path AND c.id <> base.id))
  AND (sqlc.narg(depth)::int IS NULL
       OR nlevel(c.path) <= coalesce(nlevel(base.path), 0) + sqlc.narg(depth)::int)
ORDER BY c.kind, c.path;
