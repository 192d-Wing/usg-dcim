-- +goose Up
CREATE INDEX ix_audit_log_site_action ON audit_log (site_id, action);

-- +goose Down
DROP INDEX IF EXISTS ix_audit_log_site_action;
