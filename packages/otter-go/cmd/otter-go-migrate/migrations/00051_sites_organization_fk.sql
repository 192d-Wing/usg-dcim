-- +goose Up
CREATE UNIQUE INDEX IF NOT EXISTS uq_organizations_name ON organizations (name);
ALTER TABLE sites ADD COLUMN IF NOT EXISTS organization_id UUID;

        UPDATE sites s
        SET    organization_id = o.id
        FROM   organizations o
        WHERE  s.organization IS NOT NULL
          AND  s.organization = o.name
          AND  s.organization_id IS NULL;
ALTER TABLE sites ADD CONSTRAINT fk_sites_organization_id FOREIGN KEY (organization_id) REFERENCES organizations(id);
CREATE INDEX IF NOT EXISTS ix_sites_organization_id ON sites (organization_id);

-- +goose Down
DROP INDEX IF EXISTS ix_sites_organization_id;
ALTER TABLE sites DROP CONSTRAINT IF EXISTS fk_sites_organization_id;
ALTER TABLE sites DROP COLUMN IF EXISTS organization_id;
DROP INDEX IF EXISTS uq_organizations_name;
