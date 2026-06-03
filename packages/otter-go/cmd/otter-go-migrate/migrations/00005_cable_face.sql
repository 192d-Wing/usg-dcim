-- +goose Up
ALTER TABLE cables ADD COLUMN IF NOT EXISTS face VARCHAR(8);

-- +goose Down
ALTER TABLE cables DROP COLUMN IF EXISTS face;
