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

-- BR-079. Only the service calls it, under the lock, after the pending and
-- version checks; the total is recomputed from the new shipping amount in the
-- same statement.
-- name: UpdatePendingOrder :one
UPDATE orders SET
    shipping_address = coalesce(sqlc.narg(shipping_address), shipping_address),
    note             = CASE WHEN sqlc.arg(set_note)::bool THEN sqlc.narg(note) ELSE note END,
    shipping_amount  = coalesce(sqlc.narg(shipping), shipping_amount),
    total_amount     = subtotal_amount + coalesce(sqlc.narg(shipping), shipping_amount) - discount_amount,
    version          = version + 1,
    updated_at       = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: TenantOrderPrefix :one
SELECT order_prefix FROM tenants WHERE id = current_setting('app.tenant_id')::uuid;

-- BR-077. The upsert takes the counter row's lock itself, so no seed row per
-- tenant is needed; gaps from a rolled-back insert are fine, reuse is not.
-- name: NextOrderNumber :one
INSERT INTO order_sequences (tenant_id, last_value)
VALUES (current_setting('app.tenant_id')::uuid, 1)
ON CONFLICT (tenant_id) DO UPDATE SET last_value = order_sequences.last_value + 1
RETURNING last_value;

-- BR-046, BR-078: what a manual order snapshots from the catalog. price is
-- variant_price(), the one function that decides what a variant costs now.
-- name: VariantForOrder :one
SELECT v.id, v.sku, v.option_values, v.archived_at, variant_price(v)::bigint AS price, v.weight_grams, p.title
FROM variants v JOIN products p ON p.id = v.product_id
WHERE v.id = $1;

-- name: CreateOrder :one
INSERT INTO orders (id, tenant_id, source, customer_id, cart_id, order_number, customer, shipping_address,
                    note, payment_method, shipping_courier, shipping_service,
                    subtotal_amount, shipping_amount, discount_amount, total_amount, placed_at)
VALUES (sqlc.arg(id), current_setting('app.tenant_id')::uuid, sqlc.arg(source), sqlc.narg(customer_id),
        sqlc.narg(cart_id), sqlc.arg(order_number), sqlc.arg(customer), sqlc.arg(shipping_address),
        sqlc.narg(note), sqlc.arg(payment_method), sqlc.narg(shipping_courier), sqlc.narg(shipping_service),
        sqlc.arg(subtotal), sqlc.arg(shipping), sqlc.arg(discount), sqlc.arg(total), now())
RETURNING *;

-- name: CreateOrderLine :exec
INSERT INTO order_lines (id, tenant_id, order_id, variant_id, sku_snapshot, title_snapshot,
                         qty, unit_price, discount_amount)
VALUES (sqlc.arg(id), current_setting('app.tenant_id')::uuid, sqlc.arg(order_id), sqlc.arg(variant_id),
        sqlc.arg(sku), sqlc.arg(title), sqlc.arg(qty), sqlc.arg(unit_price), sqlc.arg(discount));

-- name: OrderByCart :one
-- The order a checked-out cart made: a replayed checkout returns it (BR-088).
SELECT * FROM orders WHERE cart_id = $1;

-- name: CheckOutCart :exec
UPDATE carts SET status = 'checked_out', updated_at = now() WHERE id = $1;

-- name: CustomerOrders :many
-- A customer's history, newest first (P1-208), on the my-orders index.
SELECT o.id, o.order_number, o.status, o.total_amount, o.placed_at,
       (SELECT coalesce(sum(l.qty), 0)::int FROM order_lines l WHERE l.order_id = o.id) AS item_count
FROM orders o
WHERE o.customer_id = sqlc.arg(customer_id)
  AND (sqlc.narg(before_at)::timestamptz IS NULL
       OR (o.placed_at, o.id) < (sqlc.narg(before_at)::timestamptz, sqlc.narg(before_id)::uuid))
ORDER BY o.placed_at DESC, o.id DESC
LIMIT sqlc.arg(lim);

-- name: OrderByNumber :one
SELECT * FROM orders WHERE order_number = $1;
