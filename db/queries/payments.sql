-- Midtrans Snap attempts (P1-220, BR-123).

-- name: CreatePayment :one
INSERT INTO payments (id, tenant_id, order_id, provider_order_id, attempt, gross_amount, expires_at)
VALUES (sqlc.arg(id), current_setting('app.tenant_id')::uuid, sqlc.arg(order_id), sqlc.arg(provider_order_id),
        sqlc.arg(attempt), sqlc.arg(gross_amount), sqlc.arg(expires_at))
RETURNING *;

-- name: SetPaymentLink :exec
UPDATE payments SET snap_token = sqlc.arg(snap_token), redirect_url = sqlc.arg(redirect_url), updated_at = now()
WHERE id = sqlc.arg(id);

-- name: LatestPayment :one
SELECT * FROM payments WHERE order_id = $1 ORDER BY attempt DESC LIMIT 1;

-- name: OrderPayments :many
-- Newest first, as the admin order detail lists them.
SELECT * FROM payments WHERE order_id = $1 ORDER BY attempt DESC;
