-- Local use only. Migrations are forward-only in production -- see CLAUDE.md.
ALTER TABLE tenants DROP COLUMN order_prefix;
ALTER TABLE tenants ADD COLUMN currency char(3) NOT NULL DEFAULT 'IDR';
