-- P1-082: tenants v2 (03-erd.md 3.2).
--
-- currency goes: IDR is the only currency, and a tenant has no setting for it
-- (BR-029). Money columns keep their own char(3) so a second currency later is
-- a feature, not a migration (BR-006).
--
-- order_prefix arrives: the prefix of the human order number, 'TKA' ->
-- TKA-000123 (BR-077). Existing tenants get one derived from their slug, which
-- the owner can change in settings; a new prefix applies to new orders only.
ALTER TABLE tenants DROP COLUMN currency;

ALTER TABLE tenants ADD COLUMN order_prefix text;
UPDATE tenants SET order_prefix = (
    SELECT CASE WHEN length(p) < 2 THEN rpad(p, 2, 'X') ELSE p END
      FROM (SELECT upper(left(regexp_replace(slug, '[^a-zA-Z0-9]', '', 'g'), 6)) AS p) s
);
ALTER TABLE tenants ALTER COLUMN order_prefix SET NOT NULL;
ALTER TABLE tenants ADD CONSTRAINT tenants_order_prefix_check
    CHECK (order_prefix ~ '^[A-Z0-9]{2,6}$');
