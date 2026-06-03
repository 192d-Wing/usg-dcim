-- +goose Up
ALTER TABLE dhcp_servers ADD COLUMN IF NOT EXISTS auto_push BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE dhcp_servers DROP COLUMN IF EXISTS auto_push;
