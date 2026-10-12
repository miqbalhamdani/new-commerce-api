DELETE FROM jobs WHERE kind = 'customer_purge';
ALTER TABLE jobs DROP CONSTRAINT jobs_kind_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_kind_check
    CHECK (kind IN ('product_import','order_export','channel_import','image_derivatives'));
ALTER TABLE customers DROP COLUMN purged_at;
