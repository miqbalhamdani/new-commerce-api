-- Local use only. Migrations are forward-only in production -- see CLAUDE.md.
--
-- Reverse creation order: refresh_tokens and api_keys reference users, and all
-- three reference tenants.

DROP TABLE IF EXISTS api_keys;
DROP TABLE IF EXISTS refresh_tokens;
DROP TABLE IF EXISTS users;

ALTER TABLE tenants DROP CONSTRAINT IF EXISTS tenants_id_uq;
DROP TABLE IF EXISTS tenants;
