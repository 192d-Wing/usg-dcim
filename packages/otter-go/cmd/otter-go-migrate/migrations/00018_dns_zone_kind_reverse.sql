-- +goose Up
ALTER TYPE dns_zone_kind ADD VALUE IF NOT EXISTS 'reverse';

-- +goose Down
-- (no-op downgrade)
