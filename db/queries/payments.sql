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

-- name: PaymentByProviderOrderID :one
-- Read without a lock: the webhook locks the order first, then this row, the
-- order pay-again locks in too.
SELECT * FROM payments WHERE provider_order_id = $1;

-- name: LockPayment :one
SELECT * FROM payments WHERE id = $1 FOR UPDATE;

-- name: RecordNotification :exec
-- What Get Status confirmed, the verified body, and where that leaves the
-- attempt (BR-124, BR-125).
UPDATE payments SET status = sqlc.arg(status), transaction_status = sqlc.arg(transaction_status),
       last_notification = sqlc.arg(last_notification), paid_at = sqlc.narg(paid_at), updated_at = now()
WHERE id = sqlc.arg(id);

-- name: FailStalePayment :exec
-- A pending attempt with no live link makes way for the next (BR-123: one
-- live attempt per order).
UPDATE payments SET status = 'failed', updated_at = now() WHERE id = $1 AND status = 'pending';
