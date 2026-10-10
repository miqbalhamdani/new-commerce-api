-- Orders (Phase 2, 04-api-spec.md §5). Status changes only through the one
-- Transition in internal/orders (BR-071): it takes the row lock below, checks
-- the allow-list and is the only caller of TransitionOrder. A bare
-- "UPDATE orders SET status" anywhere else is a bug by definition.

-- name: LockOrder :one
SELECT * FROM orders WHERE id = $1 FOR UPDATE;

-- name: GetOrder :one
SELECT * FROM orders WHERE id = $1;

-- BR-071. Stamps the timestamp matching the target status (processing has
-- none) and bumps version. courier and tracking_number arrive only on a ship
-- (BR-072); coalesce keeps them on every other move.
-- name: TransitionOrder :one
UPDATE orders SET
    status          = sqlc.arg(to_status),
    paid_at         = CASE WHEN sqlc.arg(to_status)::text = 'paid'      THEN now() ELSE paid_at END,
    shipped_at      = CASE WHEN sqlc.arg(to_status)::text = 'shipped'   THEN now() ELSE shipped_at END,
    completed_at    = CASE WHEN sqlc.arg(to_status)::text = 'completed' THEN now() ELSE completed_at END,
    cancelled_at    = CASE WHEN sqlc.arg(to_status)::text = 'cancelled' THEN now() ELSE cancelled_at END,
    courier         = coalesce(sqlc.narg(courier), courier),
    tracking_number = coalesce(sqlc.narg(tracking_number), tracking_number),
    version         = version + 1,
    updated_at      = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- BR-075. Not a transition: the status stays cancelled. The caller holds the
-- row lock and has checked cancelled + paid + not yet refunded; the table
-- CHECK refuses anything else regardless.
-- name: RecordRefund :one
UPDATE orders SET refunded_at = now(), version = version + 1, updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: GetOrderLines :many
SELECT * FROM order_lines WHERE order_id = $1 ORDER BY id;
