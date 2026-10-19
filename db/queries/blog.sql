-- The blog (P1-602, P1-603, P1-605; 04-api-spec.md §13, §11.11). Every query
-- runs inside InTenantTx, so RLS has already narrowed it to one tenant.

-- name: CreateBlogCategory :one
-- A NULL slug means slugify(name) (BR-130).
INSERT INTO blog_categories (id, tenant_id, name, slug, position)
VALUES (sqlc.arg(id), current_setting('app.tenant_id')::uuid, sqlc.arg(name),
        coalesce(sqlc.narg(slug)::text, slugify(sqlc.arg(name))), sqlc.arg(position))
RETURNING *;

-- name: GetBlogCategory :one
SELECT * FROM blog_categories WHERE id = $1;

-- name: UpdateBlogCategory :one
-- A sent slug wins; else a rename re-derives it (BR-030, BR-130); else it stays.
UPDATE blog_categories
SET name = sqlc.arg(name),
    slug = CASE WHEN sqlc.narg(slug)::text IS NOT NULL THEN sqlc.narg(slug)::text
                WHEN sqlc.arg(rename)::bool THEN slugify(sqlc.arg(name))
                ELSE slug END,
    position = sqlc.arg(position), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: ArchiveBlogCategory :one
-- Archiving twice keeps the first timestamp. Posts keep the category (BR-130).
UPDATE blog_categories SET archived_at = coalesce(archived_at, now()), updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ListBlogCategories :many
SELECT * FROM blog_categories
WHERE (archived_at IS NOT NULL) = sqlc.arg(archived)::bool
ORDER BY position, name, id;

-- name: CreateBlogPost :one
INSERT INTO blog_posts (id, tenant_id, category_id, author_id, title, slug, excerpt, body, tags,
                        status, is_featured, published_at)
VALUES (sqlc.arg(id), current_setting('app.tenant_id')::uuid, sqlc.narg(category_id),
        sqlc.narg(author_id), sqlc.arg(title), sqlc.arg(slug), sqlc.narg(excerpt), sqlc.arg(body),
        sqlc.arg(tags), sqlc.arg(status), sqlc.arg(is_featured), sqlc.narg(published_at))
RETURNING *;

-- name: GetBlogPost :one
SELECT * FROM blog_posts WHERE id = $1;

-- name: UpdateBlogPost :one
-- Every editable column at once: the service reads the post, applies the
-- PATCH and writes it back. Posts have no version; the last save wins (BR-010).
UPDATE blog_posts
SET category_id = sqlc.narg(category_id), author_id = sqlc.narg(author_id),
    title = sqlc.arg(title), slug = sqlc.arg(slug), excerpt = sqlc.narg(excerpt),
    body = sqlc.arg(body), cover_r2_key = sqlc.narg(cover_r2_key), tags = sqlc.arg(tags),
    status = sqlc.arg(status), is_featured = sqlc.arg(is_featured),
    published_at = sqlc.narg(published_at), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: ClearBlogFeatured :exec
-- One featured post per shop: the post that had the flag loses it (BR-131).
UPDATE blog_posts SET is_featured = false, updated_at = now()
WHERE is_featured AND id <> $1;

-- name: ArchiveBlogPost :one
-- Archiving clears the featured flag (BR-131).
UPDATE blog_posts SET archived_at = coalesce(archived_at, now()), is_featured = false,
                      updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ListBlogPosts :many
-- Keyset over (created_at, id), newest first. q is a substring match on the
-- title; the caller escapes LIKE metacharacters.
SELECT b.id, b.title, b.slug, b.status, b.published_at, b.is_featured, b.cover_r2_key,
       b.archived_at, b.created_at, b.updated_at,
       b.category_id, c.name AS category_name, b.author_id, u.name AS author_name
FROM blog_posts b
LEFT JOIN blog_categories c ON c.id = b.category_id
LEFT JOIN users u ON u.id = b.author_id
WHERE (b.archived_at IS NOT NULL) = sqlc.arg(archived)::bool
  AND (sqlc.narg(q)::text IS NULL OR b.title ILIKE '%' || sqlc.narg(q)::text || '%')
  AND (sqlc.narg(status)::text IS NULL OR b.status = sqlc.narg(status)::text)
  AND (sqlc.narg(category_id)::uuid IS NULL OR b.category_id = sqlc.narg(category_id)::uuid)
  AND (sqlc.narg(after_created)::timestamptz IS NULL
       OR (b.created_at, b.id) < (sqlc.narg(after_created)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY b.created_at DESC, b.id DESC
LIMIT sqlc.arg(lim);

-- name: BlogPostRefNames :one
-- The names a post's category and author show as in the admin.
SELECT c.name AS category_name, u.name AS author_name
FROM blog_posts b
LEFT JOIN blog_categories c ON c.id = b.category_id
LEFT JOIN users u ON u.id = b.author_id
WHERE b.id = $1;

-- name: ListBlogPostProducts :many
-- In the post's order, hidden products included, so the editor can remove one
-- (BR-134). cover_key is the first image's 200 px derivative, '' until made.
SELECT p.id, p.title,
       coalesce((SELECT m.derivatives->>'200' FROM product_media m
                 WHERE m.product_id = p.id ORDER BY m.position, m.id LIMIT 1), '')::text AS cover_key
FROM blog_post_products bp
JOIN products p ON p.id = bp.product_id
WHERE bp.post_id = $1
ORDER BY bp.position, p.id;

-- name: DeleteBlogPostProducts :exec
DELETE FROM blog_post_products WHERE post_id = $1;

-- name: AddBlogPostProduct :exec
INSERT INTO blog_post_products (tenant_id, post_id, product_id, position)
VALUES (current_setting('app.tenant_id')::uuid, sqlc.arg(post_id), sqlc.arg(product_id), sqlc.arg(position));

-- name: CountProducts :one
-- How many of these ids are products of this tenant (RLS), any status.
SELECT count(*) FROM products WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: StorefrontBlogCategories :many
SELECT slug, name FROM blog_categories WHERE archived_at IS NULL ORDER BY position, name, id;

-- name: StorefrontBlogPosts :many
-- The storefront's one post read (BR-081): the list, a post by slug, and its
-- related posts. Posts come only from storefront_blog_posts; an archived
-- category reads as none (BR-130).
SELECT p.id, p.slug, p.title, p.excerpt, p.body, p.cover_r2_key, p.tags, p.is_featured,
       p.published_at, p.category_id, p.author_name, p.author_bio,
       c.slug AS category_slug, c.name AS category_name
FROM storefront_blog_posts p
LEFT JOIN blog_categories c ON c.id = p.category_id AND c.archived_at IS NULL
WHERE (sqlc.narg(slug)::text IS NULL OR p.slug = sqlc.narg(slug)::text)
  AND (sqlc.narg(category)::text IS NULL OR c.slug = sqlc.narg(category)::text)
  AND (sqlc.narg(category_id)::uuid IS NULL OR p.category_id = sqlc.narg(category_id)::uuid)
  AND (sqlc.narg(not_id)::uuid IS NULL OR p.id <> sqlc.narg(not_id)::uuid)
  AND (sqlc.narg(tag)::text IS NULL OR sqlc.narg(tag)::text = ANY(p.tags))
  AND (sqlc.narg(featured)::bool IS NULL OR p.is_featured = sqlc.narg(featured)::bool)
  AND (sqlc.narg(after_published)::timestamptz IS NULL
       OR (p.published_at, p.id) < (sqlc.narg(after_published)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY p.published_at DESC, p.id DESC
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: CountStorefrontBlogPosts :one
-- The list's filters without the cursor, so the total cannot drift from it.
SELECT count(*)
FROM storefront_blog_posts p
LEFT JOIN blog_categories c ON c.id = p.category_id AND c.archived_at IS NULL
WHERE (sqlc.narg(category)::text IS NULL OR c.slug = sqlc.narg(category)::text)
  AND (sqlc.narg(tag)::text IS NULL OR sqlc.narg(tag)::text = ANY(p.tags))
  AND (sqlc.narg(featured)::bool IS NULL OR p.is_featured = sqlc.narg(featured)::bool);

-- name: BlogPostProductIDs :many
SELECT product_id FROM blog_post_products WHERE post_id = $1 ORDER BY position, product_id;
