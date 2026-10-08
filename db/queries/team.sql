-- Team: users and the tenant's own settings (P1-064, P1-071, P1-226).

-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: GetTenant :one
-- tenants has no RLS; the id comes from the caller's own tenant context.
SELECT * FROM tenants WHERE id = $1;
