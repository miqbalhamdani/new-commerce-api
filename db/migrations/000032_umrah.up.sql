-- Phase 13: umrah packages (03-erd.md §3.2, §3.3, §3.5, §4). One migration
-- for the phase's schema; each column says which rule it serves.

-- BR-170. Every shop that exists now is retail; only the platform console
-- makes one umrah.
ALTER TABLE tenants
    ADD COLUMN business_type text NOT NULL DEFAULT 'retail' CHECK (business_type IN ('retail','umrah'));

-- BR-171. Package details, NULL for every retail product; the API checks the
-- shape. price_usd is display only and never reaches variant_price().
ALTER TABLE products ADD COLUMN package jsonb;
ALTER TABLE variants ADD COLUMN price_usd bigint CHECK (price_usd > 0);

-- BR-142's licence and an umrah shop's policies; BR-098's rebuild hook, its
-- host list checked by the API, and the worker's record of the last call.
ALTER TABLE storefront_settings
    ADD COLUMN license               text CHECK (char_length(license) <= 120),
    ADD COLUMN cancellation_policy   text CHECK (char_length(cancellation_policy) <= 3000),
    ADD COLUMN document_requirements text CHECK (char_length(document_requirements) <= 3000),
    ADD COLUMN rebuild_hook_url      text CHECK (char_length(rebuild_hook_url) <= 500),
    ADD COLUMN rebuild_last_at       timestamptz,
    ADD COLUMN rebuild_last_status   integer;

-- The storefront views gain the two new columns at their ends, which CREATE
-- OR REPLACE allows without dropping anything that depends on them.
CREATE OR REPLACE VIEW storefront_products WITH (security_invoker = true) AS
SELECT p.id, p.tenant_id, p.slug, p.title, p.description, p.brand_id,
       p.attributes, p.option_names, p.updated_at,
       p.package
FROM products p
WHERE p.status = 'active' AND p.archived_at IS NULL;

CREATE OR REPLACE VIEW storefront_variants WITH (security_invoker = true) AS
SELECT v.id, v.tenant_id, v.product_id, v.sku, v.option_values,
       variant_price(v)::bigint                    AS price_amount,
       v.regular_price_amount,
       variant_price(v) < v.regular_price_amount   AS on_sale,
       v.currency, v.weight_grams,
       sp.title AS product_title,
       v.price_usd
FROM variants v
JOIN storefront_products sp ON sp.id = v.product_id
WHERE v.archived_at IS NULL;
