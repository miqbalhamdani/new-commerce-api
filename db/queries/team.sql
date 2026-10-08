-- Team: users and the tenant's own settings (P1-064, P1-071, P1-226).

-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: GetTenant :one
-- tenants has no RLS; the id comes from the caller's own tenant context.
SELECT * FROM tenants WHERE id = $1;

-- name: ListUsers :many
SELECT * FROM users
WHERE (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
  AND (sqlc.narg(role)::text IS NULL OR role = sqlc.narg(role)::text)
  AND (sqlc.narg(after_at)::timestamptz IS NULL
       OR (created_at, id) > (sqlc.narg(after_at)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY created_at, id
LIMIT sqlc.arg(lim);

-- name: InviteUser :one
INSERT INTO users (id, tenant_id, email, name, role, status)
VALUES (sqlc.arg(id), current_setting('app.tenant_id')::uuid, sqlc.arg(email), sqlc.arg(name), sqlc.arg(role), 'invited')
RETURNING *;

-- name: SetUserRole :one
UPDATE users SET role = sqlc.arg(role) WHERE id = sqlc.arg(id) RETURNING *;

-- name: SetUserStatus :one
UPDATE users SET status = sqlc.arg(status) WHERE id = sqlc.arg(id) RETURNING *;

-- name: OtherActiveOwners :one
-- BR-027: the last active owner cannot be demoted or disabled.
SELECT count(*)::int FROM users WHERE role = 'owner' AND status = 'active' AND id <> $1;

-- name: AcceptInvitation :one
-- Only an invited user accepts, so a link stops working once it has been used
-- (BR-026).
UPDATE users SET password_hash = sqlc.arg(password_hash), status = 'active'
WHERE id = sqlc.arg(id) AND status = 'invited'
RETURNING *;

-- name: UpdateSettings :one
-- tenants has no RLS, so the WHERE names the caller's own tenant explicitly.
UPDATE tenants SET
  name         = coalesce(sqlc.narg(name), name),
  timezone     = coalesce(sqlc.narg(timezone), timezone),
  order_prefix = coalesce(sqlc.narg(order_prefix), order_prefix)
WHERE id = current_setting('app.tenant_id')::uuid
RETURNING *;
