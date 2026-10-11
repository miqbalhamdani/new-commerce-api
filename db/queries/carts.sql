-- Storefront carts (P1-204, 04-api-spec.md §11.5). A cart stores no prices:
-- every read prices it now through storefront_variants (BR-046, BR-087).

-- name: CreateCart :one
INSERT INTO carts (id, tenant_id, customer_id, expires_at)
VALUES (sqlc.arg(id), current_setting('app.tenant_id')::uuid, sqlc.narg(customer_id), now() + interval '30 days')
RETURNING *;

-- name: GetCart :one
SELECT * FROM carts WHERE id = $1;

-- name: LockCart :one
-- By id only, never with a status filter: a waiter re-checks a filter against
-- the row the lock holder committed, and would find nothing (P1-205).
SELECT * FROM carts WHERE id = $1 FOR UPDATE;

-- name: TouchCart :exec
-- Any change moves the expiry to 30 days out (BR-087).
UPDATE carts SET expires_at = now() + interval '30 days', updated_at = now() WHERE id = $1;

-- name: SetCartItem :exec
INSERT INTO cart_items (tenant_id, cart_id, variant_id, qty)
VALUES (current_setting('app.tenant_id')::uuid, sqlc.arg(cart_id), sqlc.arg(variant_id), sqlc.arg(qty))
ON CONFLICT (cart_id, variant_id) DO UPDATE SET qty = EXCLUDED.qty;

-- name: DeleteCartItem :exec
DELETE FROM cart_items WHERE cart_id = $1 AND variant_id = $2;

-- name: VariantVisible :one
SELECT EXISTS (SELECT 1 FROM storefront_variants WHERE id = $1);

-- name: CartLines :many
-- Each item named through the labels view, so one that has gone out of view
-- still says what it was (BR-090), and priced through storefront_variants
-- only while it is visible.
SELECT ci.variant_id, ci.qty, l.sku, l.option_values, l.product_title, l.product_slug, l.available,
       sv.price_amount
FROM cart_items ci
JOIN storefront_variant_labels l ON l.id = ci.variant_id
LEFT JOIN storefront_variants sv ON sv.id = ci.variant_id
WHERE ci.cart_id = $1
ORDER BY ci.added_at, ci.variant_id;

-- name: CartImages :many
-- One image per item that has one: the variant's own, else its product's
-- first; the 200 px rendition, or the original until it exists.
SELECT DISTINCT ON (ci.variant_id) ci.variant_id, coalesce(m.derivatives->>'200', m.r2_key)::text AS image_key
FROM cart_items ci
LEFT JOIN storefront_variants sv ON sv.id = ci.variant_id
JOIN product_media m ON m.variant_id = ci.variant_id OR m.product_id = sv.product_id
WHERE ci.cart_id = $1
ORDER BY ci.variant_id, (m.variant_id = ci.variant_id) IS TRUE DESC, m.position, m.id;

-- name: ClaimCart :exec
-- A signed-in shopper's cart becomes theirs if it is nobody's yet; it is
-- never taken from another customer (BR-095).
UPDATE carts SET customer_id = sqlc.arg(customer_id)
WHERE id = sqlc.arg(id) AND customer_id IS NULL AND status = 'open';
