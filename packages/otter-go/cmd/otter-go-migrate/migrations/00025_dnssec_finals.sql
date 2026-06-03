-- +goose Up
ALTER TABLE dns_zones ADD COLUMN zsk_rotation_days INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE dns_zones DROP COLUMN IF EXISTS zsk_rotation_days;
