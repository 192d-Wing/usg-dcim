-- +goose Up
ALTER TABLE dns_zones DROP CONSTRAINT uq_dns_zone_fabric_site_kind;
CREATE UNIQUE INDEX uq_dns_zone_one_apex_per_fabric ON dns_zones (fabric_id) WHERE kind = 'apex';

-- +goose Down
DROP INDEX IF EXISTS uq_dns_zone_one_apex_per_fabric;
ALTER TABLE dns_zones ADD CONSTRAINT uq_dns_zone_fabric_site_kind UNIQUE (fabric_id, site_id, kind);
