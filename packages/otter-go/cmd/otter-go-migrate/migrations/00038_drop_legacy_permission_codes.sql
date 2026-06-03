-- +goose Up
ALTER TABLE roles DROP COLUMN IF EXISTS legacy_permission_codes;

-- +goose Down
ALTER TABLE roles ADD COLUMN IF NOT EXISTS legacy_permission_codes JSON;
