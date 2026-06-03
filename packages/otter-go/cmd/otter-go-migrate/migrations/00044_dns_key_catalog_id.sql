-- +goose Up
ALTER TABLE dns_keys ADD COLUMN catalog_id UUID REFERENCES dns_catalog_zones(id) ON DELETE CASCADE;
ALTER TABLE dns_keys ALTER COLUMN zone_id DROP NOT NULL;
CREATE INDEX ix_dns_keys_catalog ON dns_keys(catalog_id);
ALTER TABLE dns_keys ADD CONSTRAINT ck_dns_keys_scope CHECK ((zone_id IS NOT NULL) != (catalog_id IS NOT NULL));

-- +goose Down
ALTER TABLE dns_keys DROP CONSTRAINT IF EXISTS ck_dns_keys_scope;
DROP INDEX IF EXISTS ix_dns_keys_catalog;
ALTER TABLE dns_keys ALTER COLUMN zone_id SET NOT NULL;
ALTER TABLE dns_keys DROP COLUMN IF EXISTS catalog_id;
