-- name: InsertAuditLog :exec
-- tenant_id comes from the transaction's own tenant setting rather than a
-- parameter, so an audit row cannot name a tenant other than the one whose
-- write it records. RLS's WITH CHECK would refuse a mismatch anyway.
INSERT INTO audit_log (tenant_id, actor_id, action, subject_type, subject_id, before, after, ip)
VALUES (current_setting('app.tenant_id')::uuid,
        sqlc.narg('actor_id'), sqlc.arg('action'), sqlc.arg('subject_type'), sqlc.arg('subject_id'),
        sqlc.narg('before'), sqlc.narg('after'), sqlc.narg('ip'));

-- name: ListAudit :many
-- Newest first over (created_at, id); the id stays internal (BR-005) and is
-- only the cursor's tie-break.
SELECT a.id, a.action, a.actor_id, u.name AS actor_name, a.subject_type, a.subject_id,
       a.before, a.after, coalesce(host(a.ip), '')::text AS ip, a.created_at
FROM audit_log a
LEFT JOIN users u ON u.id = a.actor_id
WHERE (sqlc.narg(subject_type)::text IS NULL OR a.subject_type = sqlc.narg(subject_type)::text)
  AND (sqlc.narg(subject_id)::text IS NULL OR a.subject_id = sqlc.narg(subject_id)::text)
  AND (sqlc.narg(actor_id)::uuid IS NULL OR a.actor_id = sqlc.narg(actor_id)::uuid)
  AND (sqlc.narg(from_at)::timestamptz IS NULL OR a.created_at >= sqlc.narg(from_at)::timestamptz)
  AND (sqlc.narg(to_at)::timestamptz IS NULL OR a.created_at < sqlc.narg(to_at)::timestamptz)
  AND (sqlc.narg(before_at)::timestamptz IS NULL
       OR (a.created_at, a.id) < (sqlc.narg(before_at)::timestamptz, sqlc.narg(before_id)::bigint))
ORDER BY a.created_at DESC, a.id DESC
LIMIT sqlc.arg(lim);

-- name: OrderAuditTrail :many
-- The order detail embeds its own trail (04-api-spec.md §5.2): readable with
-- orders:read alone, so ops see it without audit_log:read.
SELECT a.action, a.actor_id, u.name AS actor_name, a.before, a.after, a.created_at
FROM audit_log a
LEFT JOIN users u ON u.id = a.actor_id
WHERE a.subject_type = 'order' AND a.subject_id = $1
ORDER BY a.created_at DESC, a.id DESC;
