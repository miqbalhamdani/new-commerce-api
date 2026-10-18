-- The storefront's catalog reads (P1-203, 04-api-spec.md §11.4). Products
-- and variants only through the storefront views (BR-081); brands,
-- categories and media directly, archived rows left out (03-erd.md §4). The
-- product list is hand-written in internal/catalog/storefront.go.

-- name: StorefrontProductBySlug :one
SELECT sp.id, sp.slug, sp.title, sp.description, sp.attributes, sp.option_names, sp.updated_at,
       b.slug AS brand_slug, b.name AS brand_name
FROM storefront_products sp
LEFT JOIN brands b ON b.id = sp.brand_id AND b.archived_at IS NULL
WHERE sp.slug = $1;

-- name: StorefrontVariantsOfProduct :many
SELECT id, sku, option_values, price_amount, regular_price_amount, on_sale, weight_grams
FROM storefront_variants
WHERE product_id = $1
ORDER BY id;

-- name: StorefrontProductCategories :many
SELECT c.kind, c.name, c.path::text AS path
FROM product_categories pc
JOIN categories c ON c.id = pc.category_id
WHERE pc.product_id = $1 AND c.archived_at IS NULL
ORDER BY c.kind, c.path;

-- name: StorefrontCategoriesOfProducts :many
-- The product list's categories for one page of products, in
-- StorefrontProductCategories' order (P1-230).
SELECT pc.product_id, c.kind, c.name, c.path::text AS path
FROM product_categories pc
JOIN categories c ON c.id = pc.category_id
WHERE pc.product_id = ANY(@product_ids::uuid[]) AND c.archived_at IS NULL
ORDER BY pc.product_id, c.kind, c.path;

-- name: StorefrontProductImages :many
-- An image tied to a variant the views hide keeps its place but loses the
-- variant's id: no hidden id escapes (BR-081).
SELECT sv.id AS variant_id, m.r2_key, m.derivatives
FROM product_media m
LEFT JOIN storefront_variants sv ON sv.id = m.variant_id
WHERE m.product_id = $1
ORDER BY m.position, m.id;

-- name: StorefrontCategories :many
SELECT id, parent_id, kind, name, path::text AS path
FROM categories
WHERE archived_at IS NULL
  AND (sqlc.narg(kind)::text IS NULL OR kind = sqlc.narg(kind)::text)
ORDER BY path, kind, id;

-- name: StorefrontBrands :many
SELECT slug, name FROM brands WHERE archived_at IS NULL ORDER BY name, id;
