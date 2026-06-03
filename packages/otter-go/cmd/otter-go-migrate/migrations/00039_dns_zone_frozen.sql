-- +goose Up
ALTER TABLE dns_zones ADD COLUMN frozen BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE dns_zones DROP COLUMN IF EXISTS frozen;
