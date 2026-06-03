-- +goose Up
ALTER TABLE dns_zones ADD COLUMN nsec3_salt VARCHAR(64);
ALTER TABLE dns_zones ADD COLUMN nsec3_iterations INTEGER NOT NULL DEFAULT 0;
ALTER TABLE dns_zones ADD COLUMN nsec3_opt_out BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE dns_zones DROP COLUMN IF EXISTS nsec3_opt_out;
ALTER TABLE dns_zones DROP COLUMN IF EXISTS nsec3_iterations;
ALTER TABLE dns_zones DROP COLUMN IF EXISTS nsec3_salt;
