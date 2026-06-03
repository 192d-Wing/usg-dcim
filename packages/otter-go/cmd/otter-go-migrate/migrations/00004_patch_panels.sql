-- +goose Up
ALTER TYPE asset_kind ADD VALUE IF NOT EXISTS 'patch_panel';
ALTER TABLE assets ADD COLUMN IF NOT EXISTS port_count INTEGER;

-- +goose Down
ALTER TABLE assets DROP COLUMN IF EXISTS port_count;
