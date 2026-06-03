-- +goose Up
ALTER TABLE fabrics ADD COLUMN dns_deny_networks JSON;
ALTER TABLE fabrics ADD COLUMN dns_allow_networks JSON;

-- +goose Down
ALTER TABLE fabrics DROP COLUMN IF EXISTS dns_allow_networks;
ALTER TABLE fabrics DROP COLUMN IF EXISTS dns_deny_networks;
