-- P1-026: variants and variant_price() (03-erd.md §3.3; BR-017, BR-039, BR-040, BR-046).
CREATE TABLE variants (
    id            uuid PRIMARY KEY,
    tenant_id     uuid NOT NULL REFERENCES tenants(id),
    product_id    uuid NOT NULL,
    sku           text,          -- nullable while drafting (BR-039); required to publish (BR-038)
    barcode       text,
    -- Positional against products.option_names: ['Black','S'] (BR-040)
    option_values text[] NOT NULL DEFAULT '{}',
    -- BR-046. Never read these two directly to charge anyone: the price a
    -- shopper pays is variant_price(v), below.
    regular_price_amount bigint NOT NULL DEFAULT 0 CHECK (regular_price_amount >= 0),
    sale_price_amount    bigint,          -- NULL = no sale
    sale_starts_at       timestamptz,     -- NULL = the sale is on as soon as it is set
    sale_ends_at         timestamptz,     -- NULL = the sale runs until removed
    currency      char(3) NOT NULL DEFAULT 'IDR',
    weight_grams  integer NOT NULL DEFAULT 0 CHECK (weight_grams >= 0),
    -- BR-017: no quantity column, ever.
    archived_at   timestamptz,
    version       integer NOT NULL DEFAULT 1,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    -- BR-046: a sale price is always a discount, and a schedule runs forward.
    CONSTRAINT variants_sale_below_regular CHECK (sale_price_amount IS NULL
           OR (sale_price_amount >= 0 AND sale_price_amount < regular_price_amount)),
    CONSTRAINT variants_sale_window CHECK (sale_starts_at IS NULL OR sale_ends_at IS NULL OR sale_ends_at > sale_starts_at)
);

-- BR-046. The ONE place that decides what a variant costs right now. now()
-- makes a scheduled sale start and end on its own, with no job.
CREATE FUNCTION variant_price(v variants) RETURNS bigint
LANGUAGE sql STABLE AS $$
  SELECT CASE
           WHEN v.sale_price_amount IS NOT NULL
            AND (v.sale_starts_at IS NULL OR now() >= v.sale_starts_at)
            AND (v.sale_ends_at   IS NULL OR now() <  v.sale_ends_at)
           THEN v.sale_price_amount
           ELSE v.regular_price_amount
         END;
$$;
-- BR-039. Partial unique index: many variants may have sku IS NULL while the
-- non-null ones stay unique.
CREATE UNIQUE INDEX variants_tenant_sku_uq
    ON variants (tenant_id, sku) WHERE sku IS NOT NULL;
CREATE INDEX ON variants (tenant_id, product_id);
-- BR-040: one live variant per option combination. The matrix diff keys on it.
CREATE UNIQUE INDEX variants_product_options_uq
    ON variants (product_id, option_values) WHERE archived_at IS NULL;
ALTER TABLE variants ADD CONSTRAINT variants_id_tenant_uq UNIQUE (id, tenant_id);
ALTER TABLE variants ADD CONSTRAINT variants_same_tenant_as_product     -- BR-004
    FOREIGN KEY (product_id, tenant_id) REFERENCES products (id, tenant_id) ON DELETE CASCADE;

SELECT enable_tenant_rls('variants');
