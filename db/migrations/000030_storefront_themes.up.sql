-- P1-802: the website's theme and the rest of the shop's look (03-erd.md §3.5;
-- BR-142, BR-143). Every shop starts on katalog; a colour picked before themes
-- is kept only if it is one of katalog's four, which the app now requires.
ALTER TABLE storefront_settings
    ADD COLUMN theme         text NOT NULL DEFAULT 'katalog' CHECK (theme IN ('busana','clothing',
                             'woman','katalog','katalog-hangat','furnitur','furnitur-earthy','furnitur-ceria')),
    ADD COLUMN hero_r2_key   text,
    ADD COLUMN address       text CHECK (char_length(address) <= 300),
    ADD COLUMN opening_hours text CHECK (char_length(opening_hours) <= 120),
    ADD COLUMN shopee_url    text CHECK (char_length(shopee_url) <= 300
                             AND shopee_url ~ '^https://([a-z0-9-]+\.)*shopee\.co\.id(/\S*)?$'),
    ADD COLUMN tokopedia_url text CHECK (char_length(tokopedia_url) <= 300
                             AND tokopedia_url ~ '^https://([a-z0-9-]+\.)*tokopedia\.com(/\S*)?$');

UPDATE storefront_settings SET brand_color = NULL
WHERE brand_color NOT IN ('#2f5a4c', '#9a4e3a', '#6a3e5e', '#2d4a6e');
