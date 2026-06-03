-- +goose Up
ALTER TABLE collectors
    ADD COLUMN config_overrides JSONB NOT NULL DEFAULT '{}'::jsonb;

-- +goose Down
ALTER TABLE collectors DROP COLUMN IF EXISTS config_overrides;
