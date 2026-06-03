-- +goose Up
CREATE TYPE dns_key_role AS ENUM ('ksk', 'zsk');
CREATE TYPE dns_key_algorithm AS ENUM ('ecdsap256sha256', 'ed25519', 'rsasha256');

        CREATE TABLE dns_keys (
            id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
            created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            zone_id         UUID NOT NULL REFERENCES dns_zones(id) ON DELETE CASCADE,
            role            dns_key_role NOT NULL,
            algorithm       dns_key_algorithm NOT NULL,
            private_pem     TEXT NOT NULL,
            public_key_b64  TEXT NOT NULL,
            key_tag         INTEGER NOT NULL,
            active_from     TIMESTAMPTZ NOT NULL,
            retired_at      TIMESTAMPTZ
        );
CREATE INDEX ix_dns_keys_zone ON dns_keys (zone_id);
ALTER TABLE dns_zones ADD COLUMN signed BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE dns_zones DROP COLUMN IF EXISTS signed;
DROP TABLE IF EXISTS dns_keys;
DROP TYPE IF EXISTS dns_key_algorithm;
DROP TYPE IF EXISTS dns_key_role;
