-- +goose Up
CREATE TABLE dns_catalog_zones (  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),  fabric_id UUID NOT NULL    REFERENCES fabrics(id) ON DELETE CASCADE,  name VARCHAR(253) NOT NULL,  enabled BOOLEAN NOT NULL DEFAULT TRUE,  signed BOOLEAN NOT NULL DEFAULT FALSE);
ALTER TABLE dns_catalog_zones ADD CONSTRAINT uq_dns_catalog_fabric UNIQUE (fabric_id);
ALTER TABLE dns_catalog_zones ADD CONSTRAINT uq_dns_catalog_name UNIQUE (name);
CREATE INDEX ix_dns_catalog_fabric ON dns_catalog_zones (fabric_id);

-- +goose Down
DROP TABLE IF EXISTS dns_catalog_zones;
