-- Bootstrap: extensions, the non-owning application role, and the RLS helper.
--
-- Nothing here creates a table. This migration exists so that every migration
-- after it can assume the extensions are present, that a new table is granted to
-- app_user automatically, and that turning on tenant isolation is one line.

-- ---------------------------------------------------------------------------
-- Extensions (erd.md 3.1)
-- ---------------------------------------------------------------------------

CREATE EXTENSION IF NOT EXISTS ltree;      -- categories.path
CREATE EXTENSION IF NOT EXISTS unaccent;   -- slugify
CREATE EXTENSION IF NOT EXISTS pg_trgm;    -- product title search
CREATE EXTENSION IF NOT EXISTS citext;     -- users.email

-- ---------------------------------------------------------------------------
-- app_user (tdd.md 3.1)
-- ---------------------------------------------------------------------------
--
-- The application connects as this role. It owns nothing, which is the whole
-- point: FORCE ROW LEVEL SECURITY makes a policy apply to the table owner too,
-- but a role that owns nothing can never reach for owner-only escapes -- it
-- cannot ALTER a table to drop a policy, and it cannot become the owner.
--
-- Migrations run as the owner, not as this role.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app_user') THEN
        -- No password. Local development authenticates by trust; every deployed
        -- environment sets one out of band, so a credential never lands in a
        -- migration file or in git.
        CREATE ROLE app_user LOGIN;
    END IF;
END
$$;

GRANT USAGE ON SCHEMA public TO app_user;

-- Default privileges rather than GRANT ... ON ALL TABLES. The ON ALL form only
-- covers tables that exist at the moment it runs, so every future schema item
-- would have to remember to re-grant, and the one that forgets produces a
-- permission error in production rather than in review.
--
-- These apply to objects created by the role running this migration. That role
-- must stay the same across environments -- it is the schema owner.
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO app_user;

-- Deliberately no GRANT on sequences. Keys are UUID v7 generated in Go; this
-- schema has no sequences and is not going to grow any.

-- ---------------------------------------------------------------------------
-- enable_tenant_rls (tdd.md 3.1)
-- ---------------------------------------------------------------------------
--
-- The pattern is four statements and every tenant table needs all four. Copied
-- by hand across a dozen migrations, one of them eventually gets three of the
-- four right -- and the failure mode of a missing FORCE is silent: everything
-- works, and the owner quietly sees every tenant's rows.
--
-- So a table migration says:
--
--     SELECT enable_tenant_rls('products');
--
-- INVOKER security, not DEFINER: the caller must already own the table. app_user
-- calling this gets a permission error, which is correct.

CREATE OR REPLACE FUNCTION enable_tenant_rls(target regclass)
RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
    -- Without tenant_id the policy below would raise on every row touched.
    -- Better to fail here, while someone is looking at the migration.
    IF NOT EXISTS (
        SELECT 1 FROM pg_attribute
        WHERE attrelid = target
          AND attname  = 'tenant_id'
          AND attnum   > 0
          AND NOT attisdropped
    ) THEN
        RAISE EXCEPTION
            'enable_tenant_rls: % has no tenant_id column', target;
    END IF;

    EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', target);
    EXECUTE format('ALTER TABLE %s FORCE  ROW LEVEL SECURITY', target);

    -- current_setting(..., true) returns NULL when app.tenant_id is unset, the
    -- comparison is NULL, and every row is filtered out. RLS fails closed.
    -- internal/db turns that into ErrNoTenantContext (P1-007) so it presents as
    -- an error rather than as a baffling empty result.
    --
    -- The predicate is written exactly as tdd.md 3.1 specifies. Wrapping the
    -- setting in a scalar subquery to force one evaluation per query is a known
    -- RLS optimisation, but it is a contract deviation and unproven here --
    -- P1-030 is where product-list latency gets measured.
    EXECUTE format($policy$
        CREATE POLICY tenant_isolation ON %s
            USING      (tenant_id = current_setting('app.tenant_id', true)::uuid)
            WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid)
    $policy$, target);
END;
$$;

COMMENT ON FUNCTION enable_tenant_rls(regclass) IS
    'Enable + FORCE row level security and the tenant_isolation policy on a table with a tenant_id column. See tdd.md 3.1.';
