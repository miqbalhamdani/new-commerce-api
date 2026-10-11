-- P1-203: the storefront views (03-erd.md §4, BR-080, BR-081).
--
-- Storefront handlers read products and variants only through these. They
-- encode "visible" once (BR-080) and select only shopper-facing columns, so a
-- handler cannot reach a draft, an archived row, or a column like barcode.
--
-- security_invoker = true is not optional. A view without it runs with its
-- owner's privileges, and the owner bypasses RLS: every tenant's products
-- would be visible through it. make lint-rls fails on a tenant view without it.

CREATE VIEW storefront_products WITH (security_invoker = true) AS
SELECT p.id, p.tenant_id, p.slug, p.title, p.description, p.brand_id,
       p.attributes, p.option_names, p.updated_at
FROM products p
WHERE p.status = 'active' AND p.archived_at IS NULL;

-- price_amount is variant_price(), the one definition of what a shopper pays
-- right now (BR-046). The cast keeps sqlc's type an int64.
CREATE VIEW storefront_variants WITH (security_invoker = true) AS
SELECT v.id, v.tenant_id, v.product_id, v.sku, v.option_values,
       variant_price(v)::bigint                    AS price_amount,
       v.regular_price_amount,
       variant_price(v) < v.regular_price_amount   AS on_sale,
       v.currency, v.weight_grams,
       sp.title AS product_title
FROM variants v
JOIN storefront_products sp ON sp.id = v.product_id
WHERE v.archived_at IS NULL;
