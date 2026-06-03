-- +goose Up
ALTER TABLE fabrics ADD COLUMN dns_recursive_upstreams JSON;

-- +goose Down
ALTER TABLE fabrics DROP COLUMN IF EXISTS dns_recursive_upstreams;
