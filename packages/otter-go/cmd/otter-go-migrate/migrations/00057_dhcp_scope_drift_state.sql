-- +goose Up
ALTER TABLE dhcp_scopes ADD COLUMN IF NOT EXISTS last_diff_at TIMESTAMPTZ;
ALTER TABLE dhcp_scopes ADD COLUMN IF NOT EXISTS last_diff_status VARCHAR(32);
ALTER TABLE dhcp_scopes ADD COLUMN IF NOT EXISTS last_diff_delta_json JSONB;
CREATE INDEX IF NOT EXISTS ix_dhcp_scopes_drifted ON dhcp_scopes (dhcp_server_id) WHERE last_diff_status = 'drifted';

-- +goose Down
DROP INDEX IF EXISTS ix_dhcp_scopes_drifted;
ALTER TABLE dhcp_scopes DROP COLUMN IF EXISTS last_diff_delta_json;
ALTER TABLE dhcp_scopes DROP COLUMN IF EXISTS last_diff_status;
ALTER TABLE dhcp_scopes DROP COLUMN IF EXISTS last_diff_at;
