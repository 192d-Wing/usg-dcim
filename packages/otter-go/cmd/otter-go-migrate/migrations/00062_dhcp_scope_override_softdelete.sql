-- +goose Up
ALTER TABLE dhcp_scopes ADD COLUMN IF NOT EXISTS auto_push_override BOOLEAN;
ALTER TABLE dhcp_scopes ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS ix_dhcp_scopes_live_per_server ON dhcp_scopes (dhcp_server_id) WHERE deleted_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS ix_dhcp_scopes_live_per_server;
ALTER TABLE dhcp_scopes DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE dhcp_scopes DROP COLUMN IF EXISTS auto_push_override;
