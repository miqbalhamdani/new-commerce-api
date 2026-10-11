-- P1-220: payments, one row per Midtrans Snap attempt (03-erd.md §3.6;
-- BR-122, BR-123, BR-125).

CREATE TABLE payments (
    id                uuid PRIMARY KEY,
    tenant_id         uuid NOT NULL REFERENCES tenants(id),
    order_id          uuid NOT NULL,
    provider          text NOT NULL DEFAULT 'midtrans' CHECK (provider = 'midtrans'),
    provider_order_id text NOT NULL,          -- 'ERG-000123', then 'ERG-000123-2', …
    attempt           integer NOT NULL CHECK (attempt >= 1),
    status            text NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending','paid','failed','amount_mismatch')),
    gross_amount      bigint NOT NULL,        -- always the order's total_amount
    -- Written once Midtrans answers: the attempt row comes first, so a timeout
    -- never takes a provider_order_id without a row.
    snap_token        text,
    redirect_url      text,
    expires_at        timestamptz NOT NULL,
    transaction_status text,                  -- last status confirmed by the Get Status API
    last_notification jsonb,                  -- the last verified notification body
    paid_at           timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, provider_order_id),
    UNIQUE (order_id, attempt),
    CONSTRAINT payments_same_tenant_as_order
        FOREIGN KEY (order_id, tenant_id) REFERENCES orders (id, tenant_id) ON DELETE CASCADE
);
-- BR-123: at most one live attempt per order.
CREATE UNIQUE INDEX payments_one_pending_per_order ON payments (order_id) WHERE status = 'pending';
SELECT enable_tenant_rls('payments');
