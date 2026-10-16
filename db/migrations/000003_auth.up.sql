-- Auth: make email globally unique, and open the one cross-tenant read login
-- needs.

-- ---------------------------------------------------------------------------
-- Email is unique across the system, not per tenant (BR-020)
-- ---------------------------------------------------------------------------
--
-- POST /v1/auth/login carries only an email and a password. With the same email
-- at two tenants there is nothing in the request to choose between them, so the
-- user row has to be the thing that says which tenant it belongs to.

ALTER TABLE users DROP CONSTRAINT users_tenant_id_email_key;
ALTER TABLE users ADD CONSTRAINT users_email_key UNIQUE (email);

-- ---------------------------------------------------------------------------
-- auth_lookup_user: the only read in the system that crosses tenants
-- ---------------------------------------------------------------------------
--
-- Login runs before any tenant context exists, so current_setting('app.tenant_id')
-- is NULL, the policy on users compares against NULL, and a normal query matches
-- zero rows. Something has to reach past that exactly once.
--
-- SECURITY DEFINER alone is NOT enough. FORCE ROW LEVEL SECURITY binds the
-- table's owner too, so a definer function owned by the schema owner is filtered
-- like anyone else -- unless that owner happens to be a superuser, which is true
-- on a developer's machine and must not be relied on in production. The function
-- therefore belongs to a role that carries BYPASSRLS and owns nothing else.
--
-- What keeps this small is the return type. It is five columns, fixed at
-- definition time: no name, no created_at, nothing that could be turned into a
-- cross-tenant directory. Widening it is a schema change and a contract change.

--
-- In limited mode (BR-001) there is no auth_lookup; see the end of each
-- function below.

DO $$
BEGIN
    IF db_role_mode() = 'limited' THEN
        RETURN;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'auth_lookup') THEN
        -- NOLOGIN: nothing connects as this role. It exists to own one function.
        CREATE ROLE auth_lookup NOLOGIN BYPASSRLS;
    END IF;

    GRANT SELECT (id, tenant_id, password_hash, status, role, email) ON users TO auth_lookup;

    -- Owning a function requires membership in the owning role. The role's creator
    -- holds ADMIN OPTION on it, so this is a no-op on a fresh database and a
    -- deliberate re-grant on one where the role already existed.
    EXECUTE format('GRANT auth_lookup TO %I', current_user);
END
$$;

CREATE FUNCTION auth_lookup_user(p_email citext)
RETURNS TABLE (
    id            uuid,
    tenant_id     uuid,
    password_hash text,
    status        text,
    role          text
)
LANGUAGE sql
STABLE
SECURITY DEFINER
-- Pinned so a caller cannot shadow `users` with a table of their own earlier in
-- the search path. Mandatory on any SECURITY DEFINER function.
SET search_path = pg_catalog, public
AS $$
    SELECT u.id, u.tenant_id, u.password_hash, u.status, u.role
      FROM users u
     WHERE u.email = p_email;
$$;

-- EXECUTE is granted to PUBLIC by default on a new function, which would make
-- this readable by anything that can reach the database.
DO $$
BEGIN
    IF db_role_mode() = 'strong' THEN
        ALTER FUNCTION auth_lookup_user(citext) OWNER TO auth_lookup;
        REVOKE ALL ON FUNCTION auth_lookup_user(citext) FROM PUBLIC;
        GRANT EXECUTE ON FUNCTION auth_lookup_user(citext) TO app_user;
        RETURN;
    END IF;

    -- Limited mode (BR-001, BR-003): no auth_lookup to own this, so it belongs
    -- to the schema owner and opens limited_read_bypass for its one query.
    -- plpgsql, because a non-superuser cannot put a custom setting in a
    -- function's SET clause; set_config(..., true) is undone with the
    -- transaction or savepoint if the query fails, and restored here if not.
    --
    -- app.tenant_id goes to the nil uuid, which no shop has, for the same one
    -- query. RLS evaluates tenant_isolation next to the bypass, and on a pooled
    -- connection that has run a tenant transaction before, the setting is ''
    -- rather than unset -- and ''::uuid is an error, not a false.
    CREATE OR REPLACE FUNCTION auth_lookup_user(p_email citext)
    RETURNS TABLE (id uuid, tenant_id uuid, password_hash text, status text, role text)
    LANGUAGE plpgsql
    VOLATILE
    SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $fn$
    #variable_conflict use_column
    DECLARE
        prev        text := current_setting('app.rls_bypass', true);
        prev_tenant text := current_setting('app.tenant_id', true);
    BEGIN
        PERFORM set_config('app.tenant_id', '00000000-0000-0000-0000-000000000000', true);
        PERFORM set_config('app.rls_bypass', 'on', true);
        RETURN QUERY
            SELECT u.id, u.tenant_id, u.password_hash, u.status, u.role FROM users u WHERE u.email = p_email;
        PERFORM set_config('app.rls_bypass', coalesce(prev, ''), true);
        PERFORM set_config('app.tenant_id', coalesce(prev_tenant, ''), true);
    END
    $fn$;
    REVOKE ALL ON FUNCTION auth_lookup_user(citext) FROM PUBLIC;
END
$$;

COMMENT ON FUNCTION auth_lookup_user(citext) IS
    'Cross-tenant read for the login path only (BR-003). Returns five fixed columns and nothing else.';

-- ---------------------------------------------------------------------------
-- auth_lookup_refresh_token: the same problem, for POST /v1/auth/refresh
-- ---------------------------------------------------------------------------
--
-- Refresh presents a cookie and no access token -- that is the whole reason it
-- is being called -- so it reaches refresh_tokens with no tenant context, and
-- FORCE RLS filters it to nothing exactly as it does users.
--
-- The rejected alternative was putting the tenant id in the cookie next to the
-- token, which needs no function at all. It works, because a wrong tenant makes
-- the hash lookup miss -- but it means the tenant arrives in the request, and
-- BR-003 says the tenant is derived from the token and never accepted
-- from the request. One more narrow function in the same place is more
-- consistent than a second, different mechanism.
--
-- Returns no token material: the caller already holds the token, and the hash
-- would be a verifier if it leaked.

DO $$
BEGIN
    IF db_role_mode() = 'strong' THEN
        GRANT SELECT (id, tenant_id, user_id, token_hash, expires_at, revoked_at)
            ON refresh_tokens TO auth_lookup;
    END IF;
END
$$;

CREATE FUNCTION auth_lookup_refresh_token(p_token_hash text)
RETURNS TABLE (
    id         uuid,
    tenant_id  uuid,
    user_id    uuid,
    expires_at timestamptz,
    revoked_at timestamptz
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
    SELECT t.id, t.tenant_id, t.user_id, t.expires_at, t.revoked_at
      FROM refresh_tokens t
     WHERE t.token_hash = p_token_hash;
$$;

DO $$
BEGIN
    IF db_role_mode() = 'strong' THEN
        ALTER FUNCTION auth_lookup_refresh_token(text) OWNER TO auth_lookup;
        REVOKE ALL ON FUNCTION auth_lookup_refresh_token(text) FROM PUBLIC;
        GRANT EXECUTE ON FUNCTION auth_lookup_refresh_token(text) TO app_user;
        RETURN;
    END IF;

    -- Limited mode (BR-001, BR-003): no auth_lookup to own this, so it belongs
    -- to the schema owner and opens limited_read_bypass for its one query.
    -- plpgsql, because a non-superuser cannot put a custom setting in a
    -- function's SET clause; set_config(..., true) is undone with the
    -- transaction or savepoint if the query fails, and restored here if not.
    --
    -- app.tenant_id goes to the nil uuid, which no shop has, for the same one
    -- query. RLS evaluates tenant_isolation next to the bypass, and on a pooled
    -- connection that has run a tenant transaction before, the setting is ''
    -- rather than unset -- and ''::uuid is an error, not a false.
    CREATE OR REPLACE FUNCTION auth_lookup_refresh_token(p_token_hash text)
    RETURNS TABLE (id uuid, tenant_id uuid, user_id uuid, expires_at timestamptz, revoked_at timestamptz)
    LANGUAGE plpgsql
    VOLATILE
    SECURITY DEFINER
    SET search_path = pg_catalog, public
    AS $fn$
    #variable_conflict use_column
    DECLARE
        prev        text := current_setting('app.rls_bypass', true);
        prev_tenant text := current_setting('app.tenant_id', true);
    BEGIN
        PERFORM set_config('app.tenant_id', '00000000-0000-0000-0000-000000000000', true);
        PERFORM set_config('app.rls_bypass', 'on', true);
        RETURN QUERY
            SELECT t.id, t.tenant_id, t.user_id, t.expires_at, t.revoked_at FROM refresh_tokens t WHERE t.token_hash = p_token_hash;
        PERFORM set_config('app.rls_bypass', coalesce(prev, ''), true);
        PERFORM set_config('app.tenant_id', coalesce(prev_tenant, ''), true);
    END
    $fn$;
    REVOKE ALL ON FUNCTION auth_lookup_refresh_token(text) FROM PUBLIC;
END
$$;

COMMENT ON FUNCTION auth_lookup_refresh_token(text) IS
    'Resolves a refresh token to its tenant so the rotation can proceed inside InTenantTx. Returns no token material.';
