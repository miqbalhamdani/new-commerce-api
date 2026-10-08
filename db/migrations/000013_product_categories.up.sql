-- P1-027: product_categories (03-erd.md §3.3; BR-004, BR-031). A product may
-- sit in several trees of different kinds at once.
CREATE TABLE product_categories (
    tenant_id   uuid NOT NULL REFERENCES tenants(id),
    product_id  uuid NOT NULL,
    category_id uuid NOT NULL,
    PRIMARY KEY (product_id, category_id),
    -- BR-004: both sides belong to this row's tenant.
    CONSTRAINT product_categories_same_tenant_as_product
        FOREIGN KEY (product_id, tenant_id) REFERENCES products (id, tenant_id) ON DELETE CASCADE,
    CONSTRAINT product_categories_same_tenant_as_category
        FOREIGN KEY (category_id, tenant_id) REFERENCES categories (id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX ON product_categories (tenant_id, category_id);

SELECT enable_tenant_rls('product_categories');
