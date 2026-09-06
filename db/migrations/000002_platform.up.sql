-- Platform tables: tenants, users, refresh_tokens, api_keys.
--
-- Transcribed from erd.md 3.2. Where this file and that section disagree, this
-- file is the bug.

CREATE TABLE tenants (
    id         uuid PRIMARY KEY,
    name       text NOT NULL,
    slug       text NOT NULL UNIQUE,
    timezone   text NOT NULL DEFAULT 'Asia/Jakarta',
    currency   char(3) NOT NULL DEFAULT 'IDR',
    status     text NOT NULL DEFAULT 'active'
               CHECK (status IN ('active','suspended','closed')),
    created_at timestamptz NOT NULL DEFAULT now()
);
-- No RLS on tenants itself; it is reached only through the auth path. It also
-- has no tenant_id column, so lint-rls does not look at it.

CREATE TABLE users (
    id            uuid PRIMARY KEY,
    tenant_id     uuid NOT NULL REFERENCES tenants(id),
    email         citext NOT NULL,
    password_hash text,                 -- null while an invitation is outstanding
    name          text NOT NULL,
    role          text NOT NULL DEFAULT 'viewer'
                  CHECK (role IN ('owner','admin','ops','warehouse','viewer')),
    status        text NOT NULL DEFAULT 'invited'
                  CHECK (status IN ('invited','active','disabled')),
    last_login_at timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, email)
);
ALTER TABLE tenants ADD CONSTRAINT tenants_id_uq UNIQUE (id);

CREATE TABLE refresh_tokens (
    id         uuid PRIMARY KEY,
    tenant_id  uuid NOT NULL REFERENCES tenants(id),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash text NOT NULL,           -- SHA-256; the plaintext lives only in the cookie
    -- Rotation chain: a reused (already-rotated) token means theft. Revoke the
    -- whole chain rather than just rejecting the one request.
    rotated_from uuid REFERENCES refresh_tokens(id),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (token_hash)
);
CREATE INDEX ON refresh_tokens (user_id) WHERE revoked_at IS NULL;

CREATE TABLE api_keys (
    id          uuid PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenants(id),
    name        text NOT NULL,
    key_hash    text NOT NULL UNIQUE,   -- SHA-256; plaintext shown once at creation
    key_prefix  text NOT NULL,          -- first 8 chars, so a user can identify it in a list
    permissions text[] NOT NULL DEFAULT '{}',
    created_by  uuid REFERENCES users(id),
    last_used_at timestamptz,
    revoked_at  timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- Every table above that carries tenant_id. ENABLE + FORCE + the
-- tenant_isolation policy, in one call each -- see P1-006.
SELECT enable_tenant_rls('users');
SELECT enable_tenant_rls('refresh_tokens');
SELECT enable_tenant_rls('api_keys');
