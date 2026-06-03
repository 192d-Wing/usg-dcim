-- +goose Up
ALTER TABLE dns_zones ADD COLUMN publish_cds BOOLEAN NOT NULL DEFAULT TRUE;

-- +goose Down
ALTER TABLE dns_zones DROP COLUMN IF EXISTS publish_cds;
