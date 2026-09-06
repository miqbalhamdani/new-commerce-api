-- Auth: make email globally unique, and open the one cross-tenant read login
-- needs.

-- ---------------------------------------------------------------------------
-- Email is unique across the system, not per tenant (erd.md 3.2)
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

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'auth_lookup') THEN
        -- NOLOGIN: nothing connects as this role. It exists to own one function.
        CREATE ROLE auth_lookup NOLOGIN BYPASSRLS;
    END IF;
END
$$;

GRANT SELECT (id, tenant_id, password_hash, status, role, email) ON users TO auth_lookup;

-- Owning a function requires membership in the owning role. The role's creator
-- holds ADMIN OPTION on it, so this is a no-op on a fresh database and a
-- deliberate re-grant on one where the role already existed.
DO $$
BEGIN
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

ALTER FUNCTION auth_lookup_user(citext) OWNER TO auth_lookup;

-- EXECUTE is granted to PUBLIC by default on a new function, which would make
-- this readable by anything that can reach the database.
REVOKE ALL ON FUNCTION auth_lookup_user(citext) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_lookup_user(citext) TO app_user;

COMMENT ON FUNCTION auth_lookup_user(citext) IS
    'The single cross-tenant read, for the login path only. Returns five fixed columns and nothing else. See API spec.md section 2.';

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
-- API spec.md 1 says the tenant is derived from the token and never accepted
-- from the request. One more narrow function in the same place is more
-- consistent than a second, different mechanism.
--
-- Returns no token material: the caller already holds the token, and the hash
-- would be a verifier if it leaked.

GRANT SELECT (id, tenant_id, user_id, token_hash, expires_at, revoked_at)
    ON refresh_tokens TO auth_lookup;

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

ALTER FUNCTION auth_lookup_refresh_token(text) OWNER TO auth_lookup;
REVOKE ALL ON FUNCTION auth_lookup_refresh_token(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_lookup_refresh_token(text) TO app_user;

COMMENT ON FUNCTION auth_lookup_refresh_token(text) IS
    'Resolves a refresh token to its tenant so the rotation can proceed inside InTenantTx. Returns no token material.';
