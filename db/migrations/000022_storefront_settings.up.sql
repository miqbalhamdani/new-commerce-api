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

GRANT SELECT (tenant_id, midtrans_environment, midtrans_server_key, midtrans_webhook_id)
    ON storefront_settings TO auth_lookup;

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

ALTER FUNCTION resolve_midtrans_webhook(uuid) OWNER TO auth_lookup;
REVOKE ALL ON FUNCTION resolve_midtrans_webhook(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION resolve_midtrans_webhook(uuid) TO app_user;

COMMENT ON FUNCTION resolve_midtrans_webhook(uuid) IS
    'Resolves a Midtrans notification URL to its tenant and sealed server key (BR-003, BR-124). Three fixed columns.';
