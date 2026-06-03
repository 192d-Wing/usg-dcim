-- +goose Up
DROP INDEX IF EXISTS uq_dns_zone_one_apex_per_fabric;

-- +goose Down
CREATE UNIQUE INDEX uq_dns_zone_one_apex_per_fabric ON dns_zones (fabric_id) WHERE kind = 'apex';
