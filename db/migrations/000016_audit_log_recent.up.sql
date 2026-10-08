-- P1-077: the audit log read newest first without a subject filter.
CREATE INDEX audit_log_tenant_created_idx ON audit_log (tenant_id, created_at DESC);
