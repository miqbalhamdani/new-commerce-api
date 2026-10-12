-- P1-215: customer PII purge (03-erd.md §3.2, §3.5; BR-097).

ALTER TABLE customers ADD COLUMN purged_at timestamptz;

ALTER TABLE jobs DROP CONSTRAINT jobs_kind_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_kind_check
    CHECK (kind IN ('product_import','order_export','channel_import','image_derivatives',
                    'customer_purge'));
