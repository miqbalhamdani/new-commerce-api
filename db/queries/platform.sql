-- Queries over the platform tables.
--
-- Reads only. The writes belong to the items that need them -- P1-011 for the
-- auth path, P1-064 and P1-065 for user and API-key management -- and adding
-- them here would be guessing at their shape.

-- name: GetTenantBySlug :one
-- Reached from the auth path, before any tenant context exists. tenants has no
-- RLS, deliberately: there would be nothing to scope it by.
SELECT * FROM tenants
WHERE slug = $1;

-- name: GetUserByEmail :one
-- Runs inside InTenantTx, so RLS has already narrowed this to one tenant. The
-- UNIQUE (tenant_id, email) constraint is what makes :one correct -- email
-- alone is not unique across tenants.
SELECT * FROM users
WHERE email = $1;
