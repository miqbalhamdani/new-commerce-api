-- P1-042: product_media (03-erd.md §3.3; BR-004, BR-050, BR-053).
CREATE TABLE product_media (
    id          uuid PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants(id),
    product_id  uuid NOT NULL,
    variant_id  uuid,                                 -- NULL = product level
    r2_key      text NOT NULL,     -- object key, never a URL (BR-050); contains a content hash (BR-053)
    mime_type   text NOT NULL,
    bytes       bigint NOT NULL,
    width       integer,
    height      integer,
    position    integer NOT NULL DEFAULT 0,
    derivatives jsonb NOT NULL DEFAULT '{}'::jsonb,   -- {"1600":"<key>","800":"<key>","200":"<key>"}
    source_url  text,              -- marketplace URL it was copied from (BR-106); never served
    created_at  timestamptz NOT NULL DEFAULT now(),
    -- Confirming the same upload twice is the same image, not two (P1-043).
    CONSTRAINT product_media_product_key_uq UNIQUE (product_id, r2_key),
    -- BR-004. SET NULL (variant_id) clears only the variant, never tenant_id.
    CONSTRAINT product_media_same_tenant_as_product
        FOREIGN KEY (product_id, tenant_id) REFERENCES products (id, tenant_id) ON DELETE CASCADE,
    CONSTRAINT product_media_same_tenant_as_variant
        FOREIGN KEY (variant_id, tenant_id) REFERENCES variants (id, tenant_id)
        ON DELETE SET NULL (variant_id)
);
CREATE INDEX ON product_media (tenant_id, product_id, position);

SELECT enable_tenant_rls('product_media');
