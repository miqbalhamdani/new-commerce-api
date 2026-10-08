-- Jobs (P1-060, BR-060). Every query runs in the job's tenant.

-- name: CreateJob :one
INSERT INTO jobs (id, tenant_id, kind, params, created_by)
VALUES (sqlc.arg(id), current_setting('app.tenant_id')::uuid, sqlc.arg(kind), sqlc.arg(params), sqlc.narg(created_by))
RETURNING *;

-- name: GetJob :one
SELECT * FROM jobs WHERE id = $1;

-- name: StartJob :one
-- The guard that makes a redelivered job finish once: a job already done or
-- failed is not started again.
UPDATE jobs SET state = 'running' WHERE id = $1 AND state IN ('queued', 'running') RETURNING *;

-- name: JobProgress :exec
UPDATE jobs SET processed = sqlc.arg(processed), total = sqlc.narg(total), failed = sqlc.arg(failed)
WHERE id = sqlc.arg(id) AND state = 'running';

-- name: FinishJob :exec
UPDATE jobs SET state = 'done', result = sqlc.narg(result), finished_at = now()
WHERE id = sqlc.arg(id) AND state = 'running';

-- name: FailJob :exec
UPDATE jobs SET state = 'failed', error = sqlc.arg(error), finished_at = now()
WHERE id = sqlc.arg(id) AND state IN ('queued', 'running');

-- name: CheckpointJob :exec
-- Written in the same transaction as the batch it counts, so a redelivered
-- job resumes exactly after what committed.
UPDATE jobs SET processed = sqlc.arg(processed), failed = sqlc.arg(failed), result = sqlc.arg(result)
WHERE id = sqlc.arg(id);
