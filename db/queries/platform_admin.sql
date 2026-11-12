-- BR-150..152, the platform admin. Every query here runs inside one shop's
-- InTenantTx (internal/platformadmin), so RLS scopes it to that shop exactly as
-- it scopes a request. tenants has no RLS, so a write to it names the
-- transaction's own tenant, as UpdateSettings does.

-- name: PlatformActor :one
-- The signed-in user, for the PLATFORM_ADMIN_EMAILS check: in their own shop.
SELECT email, status FROM users WHERE id = $1;

-- name: PlatformShopOwner :one
-- The shop's first owner.
SELECT id, name, email, status, last_login_at
  FROM users
 WHERE role = 'owner'
 ORDER BY created_at, id
 LIMIT 1;

-- The per-shop counts and dates are internal/db.PlatformShopStats, hand-written: sqlc
-- types a LATERAL column as non-null, and here NULL is the answer "none yet".

-- name: PlatformShopWebsite :one
-- No row until the shop first opens its storefront settings.
SELECT theme,
       (logo_r2_key IS NOT NULL)::bool       AS logo,
       (whatsapp_number IS NOT NULL)::bool   AS whatsapp,
       (coalesce(tagline, '') <> '')::bool   AS tagline
  FROM storefront_settings;

-- name: SetShopStatus :one
-- BR-151. Returns the status it replaced.
UPDATE tenants t SET status = $1
  FROM (SELECT status FROM tenants WHERE id = current_setting('app.tenant_id')::uuid FOR UPDATE) old
 WHERE t.id = current_setting('app.tenant_id')::uuid
RETURNING old.status;

-- name: RevokeShopRefreshTokens :execrows
-- BR-151: a shop taken offline signs every one of its staff out.
UPDATE refresh_tokens SET revoked_at = now() WHERE revoked_at IS NULL;

-- name: SetInvitedOwner :one
-- BR-151's hand-over: only while the owner has not confirmed. No row means
-- they have.
UPDATE users SET email = coalesce(sqlc.narg(email), email), name = coalesce(sqlc.narg(name), name)
 WHERE id = $1 AND status = 'invited' AND password_hash IS NULL
RETURNING email, name;
