-- +goose Up
ALTER TABLE alerts ALTER COLUMN site_id DROP NOT NULL;

-- +goose Down
DELETE FROM alerts WHERE site_id IS NULL;
ALTER TABLE alerts ALTER COLUMN site_id SET NOT NULL;
