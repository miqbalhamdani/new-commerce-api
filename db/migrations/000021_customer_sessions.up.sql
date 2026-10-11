-- P1-206: customer sessions (03-erd.md §3.5, BR-093).

-- One row per signed-in device. The refresh token rotates on every use (BR-093).
CREATE TABLE customer_sessions (
    id                 uuid PRIMARY KEY,
    tenant_id          uuid NOT NULL REFERENCES tenants(id),
    customer_id        uuid NOT NULL,
    refresh_token_hash text NOT NULL UNIQUE,
    -- The hash this session rotated away from. Seeing it again is reuse: revoke (BR-093).
    previous_token_hash text UNIQUE,
    expires_at         timestamptz NOT NULL,   -- 30 days after the last refresh
    revoked_at         timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT customer_sessions_same_tenant_as_customer
        FOREIGN KEY (customer_id, tenant_id) REFERENCES customers (id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX ON customer_sessions (tenant_id, customer_id) WHERE revoked_at IS NULL;
SELECT enable_tenant_rls('customer_sessions');
