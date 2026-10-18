-- The dashboard (P1-113, 04-api-spec.md §5.7, BR-066). An order counts once
-- it is paid and until it is cancelled, by the time it was placed, in
-- [period_from, period_to).

-- name: DashboardNeedsAction :one
-- The shop now, whatever the period: pending orders by payment method, and the
-- order list's saved views (refund owed is the partial index's predicate).
SELECT count(*) FILTER (WHERE status = 'pending' AND payment_method = 'midtrans')::int      AS awaiting_payment,
       count(*) FILTER (WHERE status = 'pending' AND payment_method = 'bank_transfer')::int AS transfers_to_confirm,
       count(*) FILTER (WHERE status IN ('paid', 'processing'))::int                        AS ready_to_ship,
       count(*) FILTER (WHERE status = 'shipped')::int                                      AS shipped,
       count(*) FILTER (WHERE status = 'cancelled' AND paid_at IS NOT NULL AND refunded_at IS NULL)::int AS refund_owed
FROM orders;

-- name: DashboardFigures :one
SELECT coalesce(sum(o.total_amount), 0)::bigint AS sales,
       count(*)::int                             AS orders,
       coalesce(sum((SELECT sum(l.qty) FROM order_lines l WHERE l.order_id = o.id)), 0)::bigint AS items_sold
FROM orders o
WHERE o.status IN ('paid', 'processing', 'shipped', 'completed')
  AND o.placed_at >= sqlc.arg(period_from) AND o.placed_at < sqlc.arg(period_to);

-- name: DashboardDaily :many
-- WIB days (BR-007), named explicitly rather than trusting the session's zone.
SELECT (o.placed_at AT TIME ZONE 'Asia/Jakarta')::date AS day,
       sum(o.total_amount)::bigint                     AS sales,
       count(*)::int                                   AS orders
FROM orders o
WHERE o.status IN ('paid', 'processing', 'shipped', 'completed')
  AND o.placed_at >= sqlc.arg(period_from) AND o.placed_at < sqlc.arg(period_to)
GROUP BY 1
ORDER BY 1;

-- name: DashboardBySource :many
SELECT o.source, sum(o.total_amount)::bigint AS sales, count(*)::int AS orders
FROM orders o
WHERE o.status IN ('paid', 'processing', 'shipped', 'completed')
  AND o.placed_at >= sqlc.arg(period_from) AND o.placed_at < sqlc.arg(period_to)
GROUP BY o.source;

-- name: DashboardByPaymentMethod :many
SELECT o.payment_method, sum(o.total_amount)::bigint AS sales, count(*)::int AS orders
FROM orders o
WHERE o.status IN ('paid', 'processing', 'shipped', 'completed')
  AND o.placed_at >= sqlc.arg(period_from) AND o.placed_at < sqlc.arg(period_to)
GROUP BY o.payment_method;

-- name: DashboardBuyers :many
-- When each person with a counted order in the period first ordered: the
-- account, else the order's email, else its phone. The caller counts a first
-- order before the period as returning (BR-066).
SELECT min(o.placed_at)::timestamptz AS first_at
FROM orders o
WHERE o.status IN ('paid', 'processing', 'shipped', 'completed')
  AND o.placed_at < sqlc.arg(period_to)
  AND coalesce(o.customer_id::text, lower(nullif(o.customer->>'email', '')), nullif(o.customer->>'phone', '')) IS NOT NULL
GROUP BY coalesce(o.customer_id::text, lower(nullif(o.customer->>'email', '')), nullif(o.customer->>'phone', ''))
HAVING max(o.placed_at) >= sqlc.arg(period_from);

-- name: DashboardTopProducts :many
SELECT p.id                                                   AS product_id,
       p.title,
       sum(l.qty)::bigint                                     AS items_sold,
       sum(l.qty::bigint * l.unit_price - l.discount_amount)::bigint AS sales
FROM order_lines l
JOIN orders o   ON o.id = l.order_id
JOIN variants v ON v.id = l.variant_id
JOIN products p ON p.id = v.product_id
WHERE o.status IN ('paid', 'processing', 'shipped', 'completed')
  AND o.placed_at >= sqlc.arg(period_from) AND o.placed_at < sqlc.arg(period_to)
GROUP BY p.id, p.title
ORDER BY sales DESC, p.title
LIMIT 5;
