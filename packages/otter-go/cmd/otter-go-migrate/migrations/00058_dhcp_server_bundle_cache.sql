-- +goose Up
ALTER TABLE dhcp_servers ADD COLUMN IF NOT EXISTS bundle_cache_at TIMESTAMPTZ;
ALTER TABLE dhcp_servers ADD COLUMN IF NOT EXISTS bundle_cache_etag VARCHAR(128);
ALTER TABLE dhcp_servers ADD COLUMN IF NOT EXISTS bundle_cache_json JSONB;

-- +goose Down
ALTER TABLE dhcp_servers DROP COLUMN IF EXISTS bundle_cache_json;
ALTER TABLE dhcp_servers DROP COLUMN IF EXISTS bundle_cache_etag;
ALTER TABLE dhcp_servers DROP COLUMN IF EXISTS bundle_cache_at;
