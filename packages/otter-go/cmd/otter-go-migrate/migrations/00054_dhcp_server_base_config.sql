-- +goose Up
ALTER TABLE dhcp_servers ADD COLUMN IF NOT EXISTS base_config JSONB NOT NULL DEFAULT '{}'::jsonb;

-- +goose Down
ALTER TABLE dhcp_servers DROP COLUMN IF EXISTS base_config;
