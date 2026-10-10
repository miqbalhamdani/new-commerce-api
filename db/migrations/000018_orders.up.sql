-- P1-100: customers, order_sequences, orders, order_lines (03-erd.md §3.5–3.6;
-- BR-004, BR-045, BR-070, BR-072, BR-075, BR-076, BR-077, BR-079).
-- orders.cart_id and orders_same_tenant_as_cart arrive with P1-204 (no carts
-- table exists yet). The payments table arrives with P1-220.

CREATE TABLE customers (
    id                uuid PRIMARY KEY,
    tenant_id         uuid NOT NULL REFERENCES tenants(id),
    email             text NOT NULL,
    -- argon2id (BR-022). NULL for a customer who only ever signed in with a
    -- provider (customer_identities, P1-223). The API refuses to create a
    -- customer with neither a password nor an identity (BR-127).
    password_hash     text,
    name              text NOT NULL,
    phone             text,
    email_verified_at timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    -- BR-092: per tenant, lower-case, unique.
    UNIQUE (tenant_id, email),
    CHECK (email = lower(email))
);
ALTER TABLE customers ADD CONSTRAINT customers_id_tenant_uq UNIQUE (id, tenant_id);

SELECT enable_tenant_rls('customers');

-- BR-077. One counter row per tenant; the upsert in db/queries/orders.sql
-- hands out the next number under the row lock. Gaps (a rolled-back insert)
-- are fine; reuse is not.
CREATE TABLE order_sequences (
    tenant_id  uuid PRIMARY KEY REFERENCES tenants(id),
    last_value bigint NOT NULL DEFAULT 0
);

SELECT enable_tenant_rls('order_sequences');

CREATE TABLE orders (
    id               uuid PRIMARY KEY,
    tenant_id        uuid NOT NULL REFERENCES tenants(id),
    source           text NOT NULL CHECK (source IN ('storefront','manual')),
    customer_id      uuid,                            -- NULL for guest and manual orders
    order_number     text NOT NULL,                   -- 'TKA-000123' (BR-077)
    status           text NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending','paid','processing','shipped',
                                       'completed','cancelled')),        -- BR-070
    -- BR-076. Snapshots at checkout. A customer who edits their profile later
    -- does not change what this order was sent to.
    customer         jsonb NOT NULL DEFAULT '{}'::jsonb,   -- {name, email, phone}
    shipping_address jsonb NOT NULL DEFAULT '{}'::jsonb,   -- {line1, line2, city, province, postal_code}
    note             text,
    -- subtotal is Σ qty·unit_price before any discount; discount is the summed
    -- line discounts, so the total CHECK below holds by construction (BR-078).
    subtotal_amount  bigint NOT NULL DEFAULT 0,
    shipping_amount  bigint NOT NULL DEFAULT 0,
    discount_amount  bigint NOT NULL DEFAULT 0,
    total_amount     bigint NOT NULL DEFAULT 0,
    currency         char(3) NOT NULL DEFAULT 'IDR' CHECK (currency = 'IDR'),   -- BR-029
    payment_method   text NOT NULL DEFAULT 'bank_transfer'
                     CHECK (payment_method IN ('bank_transfer','midtrans')),     -- BR-122
    -- BR-121. What the shopper chose at checkout (Biteship codes); NULL for a
    -- manual order with a typed shipping amount.
    shipping_courier text,
    shipping_service text,
    -- BR-072. Shipping is recorded, not booked. There is no shipments table.
    -- Defaults to shipping_courier when shipped; ops may change it.
    courier          text CHECK (courier ~ '^[a-z0-9_]+$'),
    tracking_number  text,
    placed_at        timestamptz NOT NULL,
    paid_at          timestamptz,
    shipped_at       timestamptz,
    completed_at     timestamptz,
    cancelled_at     timestamptz,
    refunded_at      timestamptz,       -- BR-075: a refund made outside the system
    version          integer NOT NULL DEFAULT 1,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, order_number),
    -- BR-072. The handler checks this too; the database refuses it regardless.
    CHECK (status NOT IN ('shipped','completed')
           OR (courier IS NOT NULL AND tracking_number IS NOT NULL)),
    -- BR-075. A refund is recorded only on a cancelled order that was paid.
    CHECK (refunded_at IS NULL OR (status = 'cancelled' AND paid_at IS NOT NULL)),
    CHECK (total_amount = subtotal_amount + shipping_amount - discount_amount),
    CONSTRAINT orders_same_tenant_as_customer                            -- BR-004
        FOREIGN KEY (customer_id, tenant_id) REFERENCES customers (id, tenant_id)
);
CREATE INDEX ON orders (tenant_id, status, placed_at DESC);
CREATE INDEX ON orders (tenant_id, customer_id, placed_at DESC)
    WHERE customer_id IS NOT NULL;                                   -- "my orders"
CREATE INDEX ON orders (tenant_id, placed_at DESC)
    WHERE status = 'cancelled' AND paid_at IS NOT NULL AND refunded_at IS NULL;  -- refund owed
ALTER TABLE orders ADD CONSTRAINT orders_id_tenant_uq UNIQUE (id, tenant_id);

SELECT enable_tenant_rls('orders');

CREATE TABLE order_lines (
    id              uuid PRIMARY KEY,
    tenant_id       uuid NOT NULL REFERENCES tenants(id),
    order_id        uuid NOT NULL,
    variant_id      uuid NOT NULL,
    -- BR-076. Snapshot at time of order. The product may be renamed or repriced
    -- later; the order must still show what was sold, and at what price.
    sku_snapshot    text NOT NULL,
    title_snapshot  text NOT NULL,    -- 'Erigo Basic Tee — Black / M'
    qty             integer NOT NULL CHECK (qty > 0),
    unit_price      bigint NOT NULL,
    discount_amount bigint NOT NULL DEFAULT 0,  -- manual orders only (BR-078)
    -- The CASCADE exists for the PII purge and tenant deletion jobs (BR-097)
    -- only; no request path deletes an order (BR-079). The variant FK has no
    -- cascade: archiving is an UPDATE and leaves lines intact (BR-045).
    CONSTRAINT order_lines_same_tenant_as_order                          -- BR-004
        FOREIGN KEY (order_id, tenant_id) REFERENCES orders (id, tenant_id) ON DELETE CASCADE,
    CONSTRAINT order_lines_same_tenant_as_variant
        FOREIGN KEY (variant_id, tenant_id) REFERENCES variants (id, tenant_id)
);
CREATE INDEX ON order_lines (tenant_id, order_id);
CREATE INDEX ON order_lines (tenant_id, variant_id);

SELECT enable_tenant_rls('order_lines');
