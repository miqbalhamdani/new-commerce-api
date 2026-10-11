-- Storefront API keys (P1-200, BR-028). The key itself is never stored:
-- key_hash is its SHA-256, and nothing here ever selects it.

-- name: ListApiKeys :many
-- Live keys only, newest first; with id set, that one live key.
SELECT k.id, k.name, k.allowed_origin, k.last_used_at, k.revoked_at, k.created_at,
       k.created_by, u.name AS created_by_name
FROM api_keys k
LEFT JOIN users u ON u.id = k.created_by
WHERE k.revoked_at IS NULL
  AND (sqlc.narg(id)::uuid IS NULL OR k.id = sqlc.narg(id)::uuid)
ORDER BY k.created_at DESC, k.id DESC;

-- name: CreateApiKey :exec
INSERT INTO api_keys (id, tenant_id, name, key_hash, allowed_origin, created_by)
VALUES (sqlc.arg(id), current_setting('app.tenant_id')::uuid, sqlc.arg(name), sqlc.arg(key_hash),
        sqlc.arg(allowed_origin), sqlc.narg(created_by));

-- name: UpdateApiKey :execrows
UPDATE api_keys
SET name = coalesce(sqlc.narg(name), name),
    allowed_origin = coalesce(sqlc.narg(allowed_origin), allowed_origin)
WHERE id = sqlc.arg(id) AND revoked_at IS NULL;

-- name: RevokeApiKey :execrows
UPDATE api_keys SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL;
