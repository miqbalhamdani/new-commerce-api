-- Local use only. Migrations are forward-only in production -- see CLAUDE.md.

DROP FUNCTION IF EXISTS resolve_api_key(text);
DROP TABLE api_keys;

CREATE TABLE api_keys (
    id          uuid PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants(id),
    name        text NOT NULL,
    key_hash    text NOT NULL UNIQUE,
    key_prefix  text NOT NULL,
    permissions text[] NOT NULL DEFAULT '{}',
    created_by  uuid REFERENCES users(id),
    last_used_at timestamptz,
    revoked_at  timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);
SELECT enable_tenant_rls('api_keys');
