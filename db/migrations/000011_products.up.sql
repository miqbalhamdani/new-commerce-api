-- P1-025: products (03-erd.md §3.3; BR-017, BR-037, BR-040, BR-042).
CREATE TABLE products (
    id           uuid PRIMARY KEY,
    tenant_id    uuid NOT NULL REFERENCES tenants(id),
    title        text NOT NULL,
    -- BR-042. URL handle on the owner's website: /products/erigo-basic-tee.
    -- Derived from the title on create, editable after, never follows the title.
    slug         text NOT NULL,
    description  text,
    brand_id     uuid,                                   -- nullable: not every product has a brand
    status       text NOT NULL DEFAULT 'draft'           -- BR-037
                 CHECK (status IN ('draft','active','archived')),
    -- Free-form attributes (material, fit, care). Classification is
    -- product_categories, not this column. Never put anything here you filter,
    -- sort on or need unique.
    attributes   jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- BR-040. Ordered option axes, e.g. ['Colour','Size']; Colour at position 0
    -- when present. variants.option_values is positional against THIS array.
    option_names text[] NOT NULL DEFAULT '{}',
    version      integer NOT NULL DEFAULT 1,
    archived_at  timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, slug)                             -- archived products included (BR-042)
);
CREATE INDEX ON products (tenant_id, status) WHERE archived_at IS NULL;
CREATE INDEX ON products (tenant_id, brand_id) WHERE archived_at IS NULL;
CREATE INDEX ON products USING gin (attributes jsonb_path_ops);
CREATE INDEX ON products USING gin (title gin_trgm_ops);    -- admin search box and storefront ?q=
ALTER TABLE products ADD CONSTRAINT products_id_tenant_uq UNIQUE (id, tenant_id);
ALTER TABLE products ADD CONSTRAINT products_same_tenant_as_brand      -- BR-004
    FOREIGN KEY (brand_id, tenant_id) REFERENCES brands (id, tenant_id);

SELECT enable_tenant_rls('products');
