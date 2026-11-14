-- Local use only. Migrations are forward-only in production -- see CLAUDE.md.
ALTER TABLE tenants DROP COLUMN plan_until, DROP COLUMN plan;
