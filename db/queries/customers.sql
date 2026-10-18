-- Customers (P1-106, 04-api-spec.md §5.5). Read-only: staff never create
-- accounts, set passwords or see hashes (BR-092) -- the SELECTs name their
-- columns so password_hash cannot ride along.

-- name: GetCustomer :one
SELECT c.id, c.name, c.email, c.phone, c.created_at, c.purged_at,
       (SELECT count(*)::int FROM orders o WHERE o.customer_id = c.id) AS order_count
FROM customers c WHERE c.id = $1;

-- name: ListCustomers :many
-- Keyset on id DESC: v7 ids are time-ordered, so newest first.
SELECT c.id, c.name, c.email, c.phone, c.created_at, c.purged_at,
       (SELECT count(*)::int FROM orders o WHERE o.customer_id = c.id) AS order_count
FROM customers c
WHERE (sqlc.narg(q)::text IS NULL
       OR c.name ILIKE '%' || sqlc.narg(q) || '%'
       OR c.email ILIKE '%' || sqlc.narg(q) || '%'
       OR c.phone ILIKE '%' || sqlc.narg(q) || '%')
  AND (sqlc.narg(before_id)::uuid IS NULL OR c.id < sqlc.narg(before_id)::uuid)
ORDER BY c.id DESC
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: CountCustomers :one
-- ListCustomers' total, with its q filter and no keyset (P1-231).
SELECT count(*) FROM customers c
WHERE (sqlc.narg(q)::text IS NULL
       OR c.name ILIKE '%' || sqlc.narg(q) || '%'
       OR c.email ILIKE '%' || sqlc.narg(q) || '%'
       OR c.phone ILIKE '%' || sqlc.narg(q) || '%');

-- Purge (P1-215, BR-097): one customer's personal data, in one transaction.
-- Every statement is safe to run again.

-- name: PurgeCustomer :one
-- Returns the email it erased, which finds the guest and manual orders placed
-- with it. The address is freed to sign up again.
UPDATE customers c
SET email = 'purged+' || c.id || '@invalid', name = 'Deleted customer', phone = NULL,
    password_hash = NULL, email_verified_at = NULL,
    purged_at = coalesce(c.purged_at, now()), updated_at = now()
FROM (SELECT p.id, p.email FROM customers p WHERE p.id = $1 FOR UPDATE) old
WHERE c.id = old.id
RETURNING old.email;

-- name: DeleteCustomerIdentities :exec
DELETE FROM customer_identities WHERE customer_id = $1;

-- name: DeleteCustomerSessions :exec
DELETE FROM customer_sessions WHERE customer_id = $1;

-- name: RedactCustomerOrders :many
-- Their own orders, and guest or manual ones placed with their email. The
-- address keeps its region for sales reports. version moves on, so an edit
-- made against the old copy is a 409 rather than a write of the old address.
UPDATE orders
SET customer = '{"name": "Deleted customer", "email": null, "phone": null}',
    shipping_address = jsonb_build_object('line1', '', 'line2', NULL,
        'city', shipping_address->'city', 'province', shipping_address->'province',
        'postal_code', shipping_address->'postal_code'),
    note = NULL, version = version + 1, updated_at = now()
WHERE customer_id = sqlc.arg(customer_id)
   OR (customer_id IS NULL AND lower(customer->>'email') = sqlc.arg(email)::text)
RETURNING id;

-- name: DropOrderNotifications :exec
UPDATE payments SET last_notification = NULL WHERE order_id = ANY(sqlc.arg(order_ids)::uuid[]);

-- name: RedactOrderAudit :exec
-- order.update rows record the address and note they changed.
UPDATE audit_log
SET before = CASE WHEN before ?| ARRAY['shipping_address','note']
                  THEN (before - ARRAY['shipping_address','note']) || '{"redacted": true}' ELSE before END,
    after  = CASE WHEN after ?| ARRAY['shipping_address','note']
                  THEN (after - ARRAY['shipping_address','note']) || '{"redacted": true}' ELSE after END
WHERE subject_type = 'order' AND subject_id = ANY(sqlc.arg(order_ids)::text[])
  AND (before ?| ARRAY['shipping_address','note'] OR after ?| ARRAY['shipping_address','note']);
