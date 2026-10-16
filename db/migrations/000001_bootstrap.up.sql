-- Bootstrap: extensions, the role mode, the non-owning application role, and
-- the RLS helper.
--
-- Nothing here creates a table. This migration exists so that every migration
-- after it can assume the extensions are present, that a new table is granted to
-- app_user automatically, and that turning on tenant isolation is one line.

-- ---------------------------------------------------------------------------
-- Extensions (03-erd.md §3.1)
-- ---------------------------------------------------------------------------

CREATE EXTENSION IF NOT EXISTS ltree;      -- categories.path
CREATE EXTENSION IF NOT EXISTS unaccent;   -- slugify
CREATE EXTENSION IF NOT EXISTS pg_trgm;    -- product title search
CREATE EXTENSION IF NOT EXISTS citext;     -- users.email

-- ---------------------------------------------------------------------------
-- db_role_mode (BR-001, 03-erd.md §3.1)
-- ---------------------------------------------------------------------------
--
-- 'strong', the default: app_user and auth_lookup exist, as everything below
-- describes. 'limited': a managed database whose schema owner can create
-- neither, so the app connects as the owner. Chosen here, once, from the
-- migrating connection's app.role_mode (cmd/migrate sets it from DB_ROLE_MODE),
-- and frozen into a function so a later migration cannot see a different
-- answer than this one did.
--
-- Limited mode is refused where strong mode is possible: it is a fallback for
-- a database that cannot do better, never a choice of convenience.

DO $$
DECLARE
    mode text := coalesce(nullif(current_setting('app.role_mode', true), ''), 'strong');
BEGIN
    IF mode NOT IN ('strong', 'limited') THEN
        RAISE EXCEPTION 'app.role_mode is %; want strong or limited', mode;
    END IF;
    IF mode = 'limited' AND EXISTS (
        SELECT 1 FROM pg_roles
         WHERE rolname = current_user
           AND (rolsuper OR (rolcreaterole AND rolbypassrls))
    ) THEN
        RAISE EXCEPTION 'limited-roles mode refused: % can create app_user and auth_lookup, so use the default mode', current_user;
    END IF;
    EXECUTE format($fn$
        CREATE FUNCTION db_role_mode() RETURNS text
        LANGUAGE sql IMMUTABLE
        AS $body$ SELECT %L::text $body$
    $fn$, mode);
END
$$;

COMMENT ON FUNCTION db_role_mode() IS
    'strong or limited, fixed when the schema was created (BR-001). See 03-erd.md section 3.1.';

-- rls_bypass_on(): limited mode's read bypass, as limited_read_bypass tests it.
--
-- Declared IMMUTABLE although it reads a setting, on purpose. Permissive
-- policies are OR'd, and `tenant_id = X OR <stable expression>` cannot use an
-- index on tenant_id: every tenant table would be scanned whole on every
-- query. IMMUTABLE lets the planner fold it to a constant -- false for the
-- app, so `X OR false` is `X` and the indexes serve as in the default mode.
--
-- The price: a statement keeps the value it was planned with. So the bypass
-- may only be on where everything is planned and run with it on -- inside the
-- four lookups, and in the backup's own pg_dump session -- and never in an
-- application session (BR-001). A statement planned with it on and run with it
-- off would still see every tenant.
DO $$
BEGIN
    IF db_role_mode() = 'limited' THEN
        CREATE FUNCTION rls_bypass_on() RETURNS boolean
        LANGUAGE plpgsql IMMUTABLE
        AS $body$
        BEGIN
            RETURN coalesce(current_setting('app.rls_bypass', true), '') = 'on';
        END
        $body$;
        COMMENT ON FUNCTION rls_bypass_on() IS
            'Limited-roles mode read bypass; IMMUTABLE so the planner folds it (BR-001). See 03-erd.md section 3.1.';
    END IF;
END
$$;

-- ---------------------------------------------------------------------------
-- app_user (BR-001)
-- ---------------------------------------------------------------------------
--
-- The application connects as this role. It owns nothing, which is the whole
-- point: FORCE ROW LEVEL SECURITY makes a policy apply to the table owner too,
-- but a role that owns nothing can never reach for owner-only escapes -- it
-- cannot ALTER a table to drop a policy, and it cannot become the owner.
--
-- Migrations run as the owner, not as this role.

--
-- In limited mode none of this section runs: there is no app_user, and the app
-- connects as the owner, which FORCE still binds because it is not a superuser.

DO $$
BEGIN
    IF db_role_mode() = 'limited' THEN
        RETURN;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app_user') THEN
        -- No password. Local development authenticates by trust; every deployed
        -- environment sets one out of band, so a credential never lands in a
        -- migration file or in git.
        CREATE ROLE app_user LOGIN;
    END IF;

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
END
$$;

-- Deliberately no GRANT on sequences. Keys are UUID v7 generated in Go; this
-- schema has no sequences and is not going to grow any.

-- ---------------------------------------------------------------------------
-- enable_tenant_rls (03-erd.md §3.1)
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
    -- The predicate is written exactly as 03-erd.md §3.1 specifies. Wrapping the
    -- setting in a scalar subquery to force one evaluation per query is a known
    -- RLS optimisation, but it is a contract deviation and unproven here --
    -- P1-030 is where product-list latency gets measured.
    EXECUTE format($policy$
        CREATE POLICY tenant_isolation ON %s
            USING      (tenant_id = current_setting('app.tenant_id', true)::uuid)
            WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid)
    $policy$, target);

    -- Limited mode has no BYPASSRLS role for BR-003's four lookups or for the
    -- backup's pg_dump, so each table admits every row to a reader that has
    -- set app.rls_bypass. SELECT only: no write ever passes it. Nothing in the
    -- application sets it (BR-001); see rls_bypass_on() for why it must not.
    IF db_role_mode() = 'limited' THEN
        EXECUTE format($policy$
            CREATE POLICY limited_read_bypass ON %s FOR SELECT
                USING (rls_bypass_on())
        $policy$, target);
    END IF;
END;
$$;

COMMENT ON FUNCTION enable_tenant_rls(regclass) IS
    'Enable + FORCE row level security and the tenant_isolation policy on a table with a tenant_id column. See 03-erd.md section 3.1.';
