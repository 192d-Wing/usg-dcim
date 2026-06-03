-- +goose Up
CREATE TYPE recursive_dns_engine AS ENUM ('coredns', 'hickory');
ALTER TABLE fabrics ADD COLUMN recursive_engine recursive_dns_engine NOT NULL DEFAULT 'coredns';

-- +goose Down
ALTER TABLE fabrics DROP COLUMN IF EXISTS recursive_engine;
DROP TYPE IF EXISTS recursive_dns_engine;
