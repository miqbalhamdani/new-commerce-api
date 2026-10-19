-- P1-601: the blog (03-erd.md §3.8, §4; BR-130..BR-135).

-- BR-133. The author's public line, and the target of the author FK below.
ALTER TABLE users ADD COLUMN bio text;
ALTER TABLE users ADD CONSTRAINT users_id_tenant_uq UNIQUE (id, tenant_id);

CREATE TABLE blog_categories (
    id          uuid PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants(id),
    name        text NOT NULL,
    slug        text NOT NULL,          -- slugify(name) unless the client sets one (BR-130)
    position    integer NOT NULL DEFAULT 0,          -- tab order (BR-130)
    archived_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, slug)            -- archived categories included (BR-130)
);
ALTER TABLE blog_categories ADD CONSTRAINT blog_categories_id_tenant_uq UNIQUE (id, tenant_id);

CREATE TABLE blog_posts (
    id           uuid PRIMARY KEY,
    tenant_id    uuid NOT NULL REFERENCES tenants(id),
    category_id  uuid,                  -- optional (BR-130)
    author_id    uuid,                  -- a staff user (BR-133)
    title        text NOT NULL,
    slug         text NOT NULL,         -- from the title on create, never follows it (BR-131)
    excerpt      text,                  -- NULL: worked out from body when read (BR-132)
    body         text NOT NULL DEFAULT '',            -- Markdown, stored as written (BR-135)
    cover_r2_key text,                  -- {tenant}/blogs/{post}/{hash}.{ext} (BR-053, BR-135)
    tags         text[] NOT NULL DEFAULT '{}',
    status       text NOT NULL DEFAULT 'draft'
                 CHECK (status IN ('draft','published')),
    is_featured  boolean NOT NULL DEFAULT false,
    published_at timestamptz,           -- later than now(): scheduled (BR-131)
    archived_at  timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, slug),           -- archived posts included (BR-131)
    CHECK (status = 'draft' OR published_at IS NOT NULL),          -- BR-131
    -- BR-004. SET NULL (author_id) clears only the author, never tenant_id.
    CONSTRAINT blog_posts_same_tenant_as_category
        FOREIGN KEY (category_id, tenant_id) REFERENCES blog_categories (id, tenant_id),
    CONSTRAINT blog_posts_same_tenant_as_author
        FOREIGN KEY (author_id, tenant_id) REFERENCES users (id, tenant_id)
        ON DELETE SET NULL (author_id)
);
CREATE UNIQUE INDEX blog_posts_one_featured ON blog_posts (tenant_id) WHERE is_featured;  -- BR-131
CREATE INDEX ON blog_posts (tenant_id, published_at DESC)
    WHERE status = 'published' AND archived_at IS NULL;
CREATE INDEX ON blog_posts (tenant_id, category_id);
CREATE INDEX ON blog_posts USING gin (tags);
ALTER TABLE blog_posts ADD CONSTRAINT blog_posts_id_tenant_uq UNIQUE (id, tenant_id);

-- "Shop the guide" (BR-134).
CREATE TABLE blog_post_products (
    tenant_id  uuid NOT NULL REFERENCES tenants(id),
    post_id    uuid NOT NULL,
    product_id uuid NOT NULL,
    position   integer NOT NULL DEFAULT 0,
    PRIMARY KEY (post_id, product_id),
    CONSTRAINT blog_post_products_same_tenant_as_post
        FOREIGN KEY (post_id, tenant_id) REFERENCES blog_posts (id, tenant_id) ON DELETE CASCADE,
    CONSTRAINT blog_post_products_same_tenant_as_product
        FOREIGN KEY (product_id, tenant_id) REFERENCES products (id, tenant_id) ON DELETE CASCADE
);

SELECT enable_tenant_rls('blog_categories');
SELECT enable_tenant_rls('blog_posts');
SELECT enable_tenant_rls('blog_post_products');

-- Storefront handlers read posts only through this view (BR-081, BR-131).
-- It is the one view that reads users, and takes the author's name and bio,
-- nothing else (BR-133). security_invoker as in 000019.
CREATE VIEW storefront_blog_posts WITH (security_invoker = true) AS
SELECT b.id, b.tenant_id, b.category_id, b.slug, b.title, b.excerpt, b.body, b.cover_r2_key,
       b.tags, b.is_featured, b.published_at,
       u.name AS author_name, u.bio AS author_bio
FROM blog_posts b
LEFT JOIN users u ON u.id = b.author_id
WHERE b.status = 'published' AND b.archived_at IS NULL AND b.published_at <= now();
