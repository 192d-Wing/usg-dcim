-- +goose Up
ALTER TABLE ip_addresses ADD COLUMN IF NOT EXISTS dhcp_duid VARCHAR(254);
CREATE INDEX IF NOT EXISTS ix_ip_addresses_dhcp_duid ON ip_addresses (dhcp_duid) WHERE dhcp_duid IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS ix_ip_addresses_dhcp_duid;
ALTER TABLE ip_addresses DROP COLUMN IF EXISTS dhcp_duid;
