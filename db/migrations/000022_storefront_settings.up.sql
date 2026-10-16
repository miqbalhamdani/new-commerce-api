-- P1-216: storefront_settings (03-erd.md §3.5; BR-120, BR-122, BR-127, BR-129)
-- and resolve_midtrans_webhook, the fourth pre-tenant read (BR-003).

CREATE TABLE storefront_settings (
    tenant_id              uuid PRIMARY KEY REFERENCES tenants(id),
    contact_email          text,                  -- Reply-To on every email (BR-128)
    -- BR-127. The shop's own Google OAuth client; NULL = Google sign-in off.
    google_client_id       text,
    -- BR-120. Biteship origin and the couriers offered. The API key is platform config.
    origin_postal_code     text CHECK (origin_postal_code ~ '^[0-9]{5}$'),
    shipping_couriers      text[] NOT NULL DEFAULT '{}',   -- Biteship codes: jne, jnt, sicepat, …
    -- BR-122. Payment methods offered at checkout.
    bank_transfer_enabled  boolean NOT NULL DEFAULT true,
    bank_transfer_instructions text,              -- shown by the website after checkout
    midtrans_enabled       boolean NOT NULL DEFAULT false,
    midtrans_environment   text CHECK (midtrans_environment IN ('sandbox','production')),
    midtrans_client_key    text,                  -- public by design (Snap.js)
    -- Envelope-encrypted (P1-227), never returned (BR-129).
    midtrans_server_key    bytea,
    -- Opaque id in the notification URL; how a webhook finds its tenant (BR-003).
    midtrans_webhook_id    uuid NOT NULL UNIQUE,
    updated_at             timestamptz NOT NULL DEFAULT now(),
    CHECK (NOT midtrans_enabled OR (midtrans_environment IS NOT NULL
           AND midtrans_client_key IS NOT NULL AND midtrans_server_key IS NOT NULL)),
    CHECK (bank_transfer_enabled OR midtrans_enabled)
);

-- Every existing shop gets its row now, before RLS: under FORCE RLS this
-- insert, which has no tenant set, would be refused for any owner that is not
-- a superuser -- production's. The webhook id is a random v4: it sits in a URL.
INSERT INTO storefront_settings (tenant_id, midtrans_webhook_id)
SELECT id, gen_random_uuid() FROM tenants;

SELECT enable_tenant_rls('storefront_settings');

-- ---------------------------------------------------------------------------
-- resolve_midtrans_webhook: the Midtrans notification's one pre-tenant read
-- ---------------------------------------------------------------------------
--
-- Same shape and reasons as resolve_api_key (000006): owned by auth_lookup
-- (NOLOGIN BYPASSRLS), search_path pinned, three columns fixed here. The
-- caller trusts tenant_id only once the notification's signature verifies with
-- the key it returns (BR-124). It matches any shop with a server key, so a
-- payment made before Midtrans was switched off still settles.

DO $$
BEGIN
    IF db_role_mode() = 'strong' THEN
        GRANT SELECT (tenant_id, midtrans_environment, midtrans_server_key, midtrans_webhook_id)
            ON storefront_settings TO auth_lookup;
    END IF;
END
$$;

CREATE FUNCTION resolve_midtrans_webhook(p_webhook_id uuid)
RETURNS TABLE (tenant_id uuid, midtrans_environment text, midtrans_server_key bytea)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
    SELECT s.tenant_id, s.midtrans_environment, s.midtrans_server_key
      FROM storefront_settings s
     WHERE s.midtrans_webhook_id = p_webhook_id AND s.midtrans_server_key IS NOT NULL;
$$;

DO $$
BEGIN
    IF db_role_mode() = 'strong' THEN
        ALTER FUNCTION resolve_midtrans_webhook(uuid) OWNER TO auth_lookup;
        REVOKE ALL ON FUNCTION resolve_midtrans_webhook(uuid) FROM PUBLIC;
        GRANT EXECUTE ON FUNCTION resolve_midtrans_webhook(uuid) TO app_user;
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
    CREATE OR REPLACE FUNCTION resolve_midtrans_webhook(p_webhook_id uuid)
    RETURNS TABLE (tenant_id uuid, midtrans_environment text, midtrans_server_key bytea)
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
            SELECT s.tenant_id, s.midtrans_environment, s.midtrans_server_key FROM storefront_settings s WHERE s.midtrans_webhook_id = p_webhook_id AND s.midtrans_server_key IS NOT NULL;
        PERFORM set_config('app.rls_bypass', coalesce(prev, ''), true);
        PERFORM set_config('app.tenant_id', coalesce(prev_tenant, ''), true);
    END
    $fn$;
    REVOKE ALL ON FUNCTION resolve_midtrans_webhook(uuid) FROM PUBLIC;
END
$$;

COMMENT ON FUNCTION resolve_midtrans_webhook(uuid) IS
    'Resolves a Midtrans notification URL to its tenant and sealed server key (BR-003, BR-124). Three fixed columns.';
