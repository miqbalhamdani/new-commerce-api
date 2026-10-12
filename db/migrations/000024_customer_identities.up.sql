-- P1-223: customer_identities, a customer's external logins (03-erd.md §3.5,
-- BR-127). Google only in v2.
CREATE TABLE customer_identities (
    tenant_id   uuid NOT NULL REFERENCES tenants(id),
    customer_id uuid NOT NULL,
    provider    text NOT NULL CHECK (provider IN ('google')),   -- later: 'apple', 'microsoft'
    subject     text NOT NULL,          -- the provider's permanent person id (the token's 'sub')
    email       text,                   -- email the provider reported; for support only
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, provider, subject),   -- one external account, one customer per shop
    UNIQUE (customer_id, provider),               -- one Google account per customer
    CONSTRAINT customer_identities_same_tenant_as_customer
        FOREIGN KEY (customer_id, tenant_id) REFERENCES customers (id, tenant_id) ON DELETE CASCADE
);
SELECT enable_tenant_rls('customer_identities');
