-- BR-150, the platform admin report. Every query here runs inside one shop's
-- InTenantTx (internal/platformadmin), so RLS scopes it to that shop exactly as
-- it scopes a request. None of them names a tenant.

-- name: PlatformActor :one
-- The signed-in user, for the PLATFORM_ADMIN_EMAILS check: in their own shop.
SELECT email, status FROM users WHERE id = $1;

-- name: PlatformShopOwner :one
-- The shop's first owner.
SELECT name, email, status, last_login_at
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
