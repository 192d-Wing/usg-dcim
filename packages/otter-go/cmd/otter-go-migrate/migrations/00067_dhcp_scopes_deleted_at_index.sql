-- +goose Up
CREATE INDEX ix_dhcp_scopes_tombstones ON dhcp_scopes (deleted_at) WHERE deleted_at IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS ix_dhcp_scopes_tombstones;
