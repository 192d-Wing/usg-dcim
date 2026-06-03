-- +goose Up
ALTER TABLE dns_server_metrics_samples ADD COLUMN top_names JSONB;

-- +goose Down
ALTER TABLE dns_server_metrics_samples DROP COLUMN IF EXISTS top_names;
