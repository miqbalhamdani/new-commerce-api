-- Customer accounts and sessions on the storefront (P1-206, BR-092, BR-093).
-- Every query runs in the API key's tenant: a customer is a customer of one
-- shop, so none of this crosses tenants.

-- name: CreateCustomer :one
INSERT INTO customers (id, tenant_id, email, password_hash, name, phone)
VALUES (sqlc.arg(id), current_setting('app.tenant_id')::uuid, sqlc.arg(email), sqlc.narg(password_hash),
        sqlc.arg(name), sqlc.narg(phone))
RETURNING *;

-- name: CustomerByEmail :one
SELECT * FROM customers WHERE email = $1;

-- name: CustomerByID :one
SELECT * FROM customers WHERE id = $1;

-- name: CreateCustomerSession :exec
INSERT INTO customer_sessions (id, tenant_id, customer_id, refresh_token_hash, expires_at)
VALUES (sqlc.arg(id), current_setting('app.tenant_id')::uuid, sqlc.arg(customer_id),
        sqlc.arg(refresh_token_hash), now() + interval '30 days');

-- name: LockCustomerSession :one
-- The session a refresh token is current for, or was current for before its
-- last rotation -- which is reuse (BR-093). Locked, so two refreshes with one
-- token serialise and the second sees the first's rotation.
SELECT * FROM customer_sessions
WHERE refresh_token_hash = sqlc.arg(token_hash) OR previous_token_hash = sqlc.arg(token_hash)
FOR UPDATE;

-- name: RotateCustomerSession :exec
UPDATE customer_sessions
SET previous_token_hash = refresh_token_hash, refresh_token_hash = sqlc.arg(new_hash),
    expires_at = now() + interval '30 days'
WHERE id = sqlc.arg(id);

-- name: RevokeCustomerSession :exec
UPDATE customer_sessions SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL;

-- name: RevokeCustomerSessionByToken :exec
UPDATE customer_sessions SET revoked_at = now()
WHERE customer_id = sqlc.arg(customer_id) AND refresh_token_hash = sqlc.arg(token_hash) AND revoked_at IS NULL;

-- name: LockCustomer :one
SELECT * FROM customers WHERE id = $1 FOR UPDATE;

-- name: SetCustomerPassword :execrows
-- Only while the hash is still the one a reset link was minted against: of
-- two confirms racing on one link, exactly one sets it (BR-094).
UPDATE customers SET password_hash = sqlc.arg(new_hash), updated_at = now()
WHERE id = sqlc.arg(id) AND password_hash IS NOT DISTINCT FROM sqlc.narg(old_hash);

-- name: RevokeCustomerSessions :exec
-- Every session, so a new password signs out whoever else had one (BR-094).
UPDATE customer_sessions SET revoked_at = now() WHERE customer_id = $1 AND revoked_at IS NULL;

-- name: UpdateCustomerProfile :one
-- name stays when omitted; phone stays unless set_phone, and then may be
-- cleared (BR-009).
UPDATE customers
SET name = coalesce(sqlc.narg(name), name),
    phone = CASE WHEN sqlc.arg(set_phone)::bool THEN sqlc.narg(phone) ELSE phone END,
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;
