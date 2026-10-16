-- P1-019: api_keys v2 (03-erd.md 3.2, BR-028) and resolve_api_key (BR-003).
--
-- One kind of key with one allowed origin. permissions and key_prefix are
-- gone, and nothing replaces them: lists tell keys apart by name and origin.
--
-- Rebuilt rather than altered, so the columns sit in the ERD's order. No route
-- has ever created a key (that is P1-200), so the table is empty; refuse
-- loudly if it is not, rather than drop someone's keys.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM api_keys) THEN
        RAISE EXCEPTION 'api_keys has rows; v2 keys need an allowed_origin nobody has chosen -- migrate them by hand';
    END IF;
END $$;

DROP TABLE api_keys;

CREATE TABLE api_keys (
    id              uuid PRIMARY KEY,
    tenant_id       uuid NOT NULL REFERENCES tenants(id),
    name            text NOT NULL,          -- 'Main website', 'Staging site'
    key_hash        text NOT NULL UNIQUE,   -- SHA-256; the plaintext is shown once
    -- The website's URL: exact scheme + host + port, no path, e.g.
    -- 'https://tokoabc.com'. One per key; a second origin gets its own key.
    allowed_origin  text NOT NULL CHECK (allowed_origin ~ '^https?://[^/?#]+$'),
    last_used_at    timestamptz,
    revoked_at      timestamptz,
    created_by      uuid REFERENCES users(id),
    created_at      timestamptz NOT NULL DEFAULT now()
);
SELECT enable_tenant_rls('api_keys');

-- ---------------------------------------------------------------------------
-- resolve_api_key: the storefront's one pre-tenant read (BR-003)
-- ---------------------------------------------------------------------------
--
-- A storefront request carries an API key and nothing else that names a
-- tenant; the key is what does. Same shape and same reasons as the two
-- functions in 000003: owned by auth_lookup (NOLOGIN BYPASSRLS), because a
-- definer owned by the schema owner is still bound by FORCE RLS; search_path
-- pinned; four columns fixed at definition time, no name and no hash.

DO $$
BEGIN
    IF db_role_mode() = 'strong' THEN
        GRANT SELECT (id, tenant_id, key_hash, allowed_origin, revoked_at) ON api_keys TO auth_lookup;
    END IF;
END
$$;

CREATE FUNCTION resolve_api_key(p_hash text)
RETURNS TABLE (id uuid, tenant_id uuid, allowed_origin text, revoked_at timestamptz)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
    SELECT k.id, k.tenant_id, k.allowed_origin, k.revoked_at
      FROM api_keys k
     WHERE k.key_hash = p_hash;
$$;

DO $$
BEGIN
    IF db_role_mode() = 'strong' THEN
        ALTER FUNCTION resolve_api_key(text) OWNER TO auth_lookup;
        REVOKE ALL ON FUNCTION resolve_api_key(text) FROM PUBLIC;
        GRANT EXECUTE ON FUNCTION resolve_api_key(text) TO app_user;
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
    CREATE OR REPLACE FUNCTION resolve_api_key(p_hash text)
    RETURNS TABLE (id uuid, tenant_id uuid, allowed_origin text, revoked_at timestamptz)
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
            SELECT k.id, k.tenant_id, k.allowed_origin, k.revoked_at FROM api_keys k WHERE k.key_hash = p_hash;
        PERFORM set_config('app.rls_bypass', coalesce(prev, ''), true);
        PERFORM set_config('app.tenant_id', coalesce(prev_tenant, ''), true);
    END
    $fn$;
    REVOKE ALL ON FUNCTION resolve_api_key(text) FROM PUBLIC;
END
$$;

COMMENT ON FUNCTION resolve_api_key(text) IS
    'Resolves a storefront API key hash to its tenant and allowed origin (BR-003). Four fixed columns, never the hash or the name.';
