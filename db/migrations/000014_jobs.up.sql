-- P1-060: jobs (03-erd.md §3.2, BR-060). Redis Streams delivers jobs; this row
-- is what GET /v1/jobs/{id} reads, so job state survives a Redis restart.
CREATE TABLE jobs (
    id           uuid PRIMARY KEY,
    tenant_id    uuid NOT NULL REFERENCES tenants(id),
    kind         text NOT NULL CHECK (kind IN ('product_import','order_export',
                                               'channel_import','image_derivatives')),
    state        text NOT NULL DEFAULT 'queued'
                 CHECK (state IN ('queued','running','done','failed')),
    processed    integer NOT NULL DEFAULT 0,
    total        integer,
    failed       integer NOT NULL DEFAULT 0,
    params       jsonb NOT NULL DEFAULT '{}'::jsonb,   -- r2_key, column_mapping, filters, …
    result       jsonb,                                -- counts, result_key, error_report_key
    error        jsonb,                                -- a Problem when state = 'failed'
    created_by   uuid REFERENCES users(id),
    created_at   timestamptz NOT NULL DEFAULT now(),
    finished_at  timestamptz
);
CREATE INDEX ON jobs (tenant_id, kind, created_at DESC);

SELECT enable_tenant_rls('jobs');
