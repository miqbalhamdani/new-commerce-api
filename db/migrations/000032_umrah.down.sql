-- Local use only. Migrations are forward-only in production -- see CLAUDE.md.
DROP VIEW storefront_variants;
DROP VIEW storefront_products;

CREATE VIEW storefront_products WITH (security_invoker = true) AS
SELECT p.id, p.tenant_id, p.slug, p.title, p.description, p.brand_id,
       p.attributes, p.option_names, p.updated_at
FROM products p
WHERE p.status = 'active' AND p.archived_at IS NULL;

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

ALTER TABLE storefront_settings
    DROP COLUMN rebuild_last_status, DROP COLUMN rebuild_last_at, DROP COLUMN rebuild_hook_url,
    DROP COLUMN document_requirements, DROP COLUMN cancellation_policy, DROP COLUMN license;
ALTER TABLE variants DROP COLUMN price_usd;
ALTER TABLE products DROP COLUMN package;
ALTER TABLE tenants DROP COLUMN business_type;
