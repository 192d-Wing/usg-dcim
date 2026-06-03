-- +goose Up
ALTER TABLE dhcp_scopes ADD COLUMN IF NOT EXISTS kea_subnet_id INTEGER;
CREATE UNIQUE INDEX IF NOT EXISTS uq_dhcp_scopes_server_kea_id ON dhcp_scopes (dhcp_server_id, kea_subnet_id) WHERE kea_subnet_id IS NOT NULL;
ALTER TABLE dhcp_servers ADD COLUMN IF NOT EXISTS last_push_at TIMESTAMPTZ;
ALTER TABLE dhcp_servers ADD COLUMN IF NOT EXISTS last_push_status VARCHAR(32);
ALTER TABLE dhcp_servers ADD COLUMN IF NOT EXISTS last_push_error VARCHAR(2048);

-- +goose Down
DROP INDEX IF EXISTS uq_dhcp_scopes_server_kea_id;
ALTER TABLE dhcp_scopes DROP COLUMN IF EXISTS kea_subnet_id;
ALTER TABLE dhcp_servers DROP COLUMN IF EXISTS last_push_error;
ALTER TABLE dhcp_servers DROP COLUMN IF EXISTS last_push_status;
ALTER TABLE dhcp_servers DROP COLUMN IF EXISTS last_push_at;
