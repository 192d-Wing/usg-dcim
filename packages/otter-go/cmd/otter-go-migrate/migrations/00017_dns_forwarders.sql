-- +goose Up

        CREATE TABLE dns_forwarders (
            id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
            created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            name          VARCHAR(128) NOT NULL,
            fabric_id     UUID NOT NULL REFERENCES fabrics(id) ON DELETE CASCADE,
            zone_pattern  VARCHAR(253) NOT NULL,
            upstreams     JSON NOT NULL DEFAULT '[]'::json,
            description   VARCHAR(512),
            CONSTRAINT uq_dns_forwarder_fabric_zone UNIQUE (fabric_id, zone_pattern)
        );
CREATE INDEX ix_dns_forwarders_fabric ON dns_forwarders (fabric_id);

-- +goose Down
DROP TABLE IF EXISTS dns_forwarders;
