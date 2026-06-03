-- +goose Up
ALTER TYPE dns_record_source ADD VALUE IF NOT EXISTS 'ddns';

-- +goose Down
-- (no-op downgrade)
