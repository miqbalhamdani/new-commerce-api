-- P1-703: the shop's look (03-erd.md §3.5; BR-142), for any website and the
-- hosted one at {slug}.belli.id (BR-140). Every column is optional.
ALTER TABLE storefront_settings
    ADD COLUMN logo_r2_key     text,
    ADD COLUMN whatsapp_number text CHECK (whatsapp_number ~ '^62[0-9]{8,13}$'),
    ADD COLUMN tagline         text CHECK (char_length(tagline) <= 160),
    ADD COLUMN brand_color     text CHECK (brand_color ~ '^#[0-9a-f]{6}$'),
    ADD COLUMN instagram       text CHECK (instagram ~ '^[A-Za-z0-9._]{1,30}$');
