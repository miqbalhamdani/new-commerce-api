DROP VIEW storefront_blog_posts;
DROP TABLE blog_post_products;
DROP TABLE blog_posts;
DROP TABLE blog_categories;
ALTER TABLE users DROP CONSTRAINT users_id_tenant_uq;
ALTER TABLE users DROP COLUMN bio;
