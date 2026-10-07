-- name: InsertAuditLog :exec
-- tenant_id comes from the transaction's own tenant setting rather than a
-- parameter, so an audit row cannot name a tenant other than the one whose
-- write it records. RLS's WITH CHECK would refuse a mismatch anyway.
INSERT INTO audit_log (tenant_id, actor_id, action, subject_type, subject_id, before, after, ip)
VALUES (current_setting('app.tenant_id')::uuid,
        sqlc.narg('actor_id'), sqlc.arg('action'), sqlc.arg('subject_type'), sqlc.arg('subject_id'),
        sqlc.narg('before'), sqlc.narg('after'), sqlc.narg('ip'));
