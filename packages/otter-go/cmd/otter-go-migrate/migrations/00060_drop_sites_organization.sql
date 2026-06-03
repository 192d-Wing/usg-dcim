-- +goose Up
DROP INDEX IF EXISTS ix_sites_organization;
ALTER TABLE sites DROP COLUMN IF EXISTS organization;

-- +goose Down
ALTER TABLE sites ADD COLUMN IF NOT EXISTS organization VARCHAR(128);
CREATE INDEX IF NOT EXISTS ix_sites_organization ON sites (organization);
