-- Customers (P1-106, 04-api-spec.md §5.5). Read-only: staff never create
-- accounts, set passwords or see hashes (BR-092) -- the SELECTs name their
-- columns so password_hash cannot ride along.

-- name: GetCustomer :one
SELECT c.id, c.name, c.email, c.phone, c.created_at,
       (SELECT count(*)::int FROM orders o WHERE o.customer_id = c.id) AS order_count
FROM customers c WHERE c.id = $1;

-- name: ListCustomers :many
-- Keyset on id DESC: v7 ids are time-ordered, so newest first.
SELECT c.id, c.name, c.email, c.phone, c.created_at,
       (SELECT count(*)::int FROM orders o WHERE o.customer_id = c.id) AS order_count
FROM customers c
WHERE (sqlc.narg(q)::text IS NULL
       OR c.name ILIKE '%' || sqlc.narg(q) || '%'
       OR c.email ILIKE '%' || sqlc.narg(q) || '%'
       OR c.phone ILIKE '%' || sqlc.narg(q) || '%')
  AND (sqlc.narg(before_id)::uuid IS NULL OR c.id < sqlc.narg(before_id)::uuid)
ORDER BY c.id DESC
LIMIT sqlc.arg(lim);
