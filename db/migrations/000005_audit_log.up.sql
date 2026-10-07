-- P1-018: audit_log (03-erd.md 3.2, BR-018). One row per admin write, in the
-- same transaction as the write, so a rollback leaves no row behind.
--
-- No foreign keys, as in the ERD: an audit row outlives the user it names.

CREATE TABLE audit_log (
    id           bigserial PRIMARY KEY, -- internal only, never in an API (BR-005)
    tenant_id    uuid NOT NULL,
    actor_id     uuid,                  -- NULL for the system (import worker, retention job)
    action       text NOT NULL,         -- 'order.transition', 'api_key.create', 'channel.import'
    subject_type text NOT NULL,
    subject_id   text NOT NULL,
    before       jsonb,
    after        jsonb,
    ip           inet,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON audit_log (tenant_id, subject_type, subject_id, created_at DESC);

SELECT enable_tenant_rls('audit_log');

-- The bootstrap grants app_user tables but deliberately no sequences; this is
-- the first bigserial, and inserting needs nextval.
GRANT USAGE ON SEQUENCE audit_log_id_seq TO app_user;
