-- P1-204: carts (03-erd.md §3.5, §4; BR-005, BR-087, BR-088, BR-090).

-- Server-side cart. The id is also the cart token the website keeps in its own
-- cookie or storage, so it is a random v4, not v7 (BR-005, BR-087).
CREATE TABLE carts (
    id          uuid PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants(id),
    customer_id uuid,                            -- set when a signed-in customer uses it (BR-095)
    status      text NOT NULL DEFAULT 'open'
                CHECK (status IN ('open','checked_out')),
    expires_at  timestamptz NOT NULL,            -- 30 days after the last change
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT carts_same_tenant_as_customer
        FOREIGN KEY (customer_id, tenant_id) REFERENCES customers (id, tenant_id)
);
CREATE INDEX ON carts (expires_at) WHERE status = 'open';
ALTER TABLE carts ADD CONSTRAINT carts_id_tenant_uq UNIQUE (id, tenant_id);
SELECT enable_tenant_rls('carts');

-- No price column. A cart always shows today's price, read from variants;
-- the price is frozen only on order_lines at checkout (BR-087).
CREATE TABLE cart_items (
    tenant_id   uuid NOT NULL REFERENCES tenants(id),
    cart_id     uuid NOT NULL,
    variant_id  uuid NOT NULL,
    qty         integer NOT NULL CHECK (qty BETWEEN 1 AND 999),
    added_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (cart_id, variant_id),         -- setting a variant again replaces its qty
    CONSTRAINT cart_items_same_tenant_as_cart
        FOREIGN KEY (cart_id, tenant_id) REFERENCES carts (id, tenant_id) ON DELETE CASCADE,
    CONSTRAINT cart_items_same_tenant_as_variant
        FOREIGN KEY (variant_id, tenant_id) REFERENCES variants (id, tenant_id)
);
SELECT enable_tenant_rls('cart_items');

-- BR-088: UNIQUE, so even a checkout that slipped past the cart's row lock
-- could not make a second order from one cart.
ALTER TABLE orders ADD COLUMN cart_id uuid UNIQUE;
ALTER TABLE orders ADD CONSTRAINT orders_same_tenant_as_cart
    FOREIGN KEY (cart_id, tenant_id) REFERENCES carts (id, tenant_id);

-- Every variant with only what a shopper saw when they added it, and whether
-- it can still be bought: what a cart line or an item_unavailable error names
-- once its variant is out of the other views (03-erd.md §4, BR-081, BR-090).
CREATE VIEW storefront_variant_labels WITH (security_invoker = true) AS
SELECT v.id, v.tenant_id, v.sku, v.option_values,
       p.title AS product_title, p.slug AS product_slug,
       (v.archived_at IS NULL AND p.status = 'active' AND p.archived_at IS NULL) AS available
FROM variants v
JOIN products p ON p.id = v.product_id;
