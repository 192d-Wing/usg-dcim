-- +goose Up
CREATE INDEX ix_dns_metrics_observed_at
    ON dns_server_metrics_samples (observed_at);

-- +goose Down
DROP INDEX IF EXISTS ix_dns_metrics_observed_at;
