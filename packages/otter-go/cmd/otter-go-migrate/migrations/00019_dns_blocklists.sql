-- +goose Up
CREATE TYPE dns_blocklist_action AS ENUM ('block', 'sinkhole');

        CREATE TABLE dns_blocklists (
            id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
            created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            name         VARCHAR(128) NOT NULL,
            fabric_id    UUID NOT NULL REFERENCES fabrics(id) ON DELETE CASCADE,
            action       dns_blocklist_action NOT NULL,
            sink_ipv4    INET,
            sink_ipv6    INET,
            enabled      BOOLEAN NOT NULL DEFAULT TRUE,
            description  VARCHAR(512),
            CONSTRAINT uq_dns_blocklist_fabric_name UNIQUE (fabric_id, name)
        );
CREATE INDEX ix_dns_blocklists_fabric ON dns_blocklists (fabric_id);

        CREATE TABLE dns_blocklist_entries (
            id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
            created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            blocklist_id  UUID NOT NULL REFERENCES dns_blocklists(id) ON DELETE CASCADE,
            pattern       VARCHAR(253) NOT NULL,
            description   VARCHAR(512),
            CONSTRAINT uq_dns_blocklist_entry_pattern UNIQUE (blocklist_id, pattern)
        );
CREATE INDEX ix_dns_blocklist_entries_blocklist ON dns_blocklist_entries (blocklist_id);

-- +goose Down
DROP TABLE IF EXISTS dns_blocklist_entries;
DROP TABLE IF EXISTS dns_blocklists;
DROP TYPE IF EXISTS dns_blocklist_action;
