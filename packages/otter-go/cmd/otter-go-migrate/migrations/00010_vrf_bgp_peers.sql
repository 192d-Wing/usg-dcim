-- +goose Up
ALTER TABLE vrfs DROP CONSTRAINT IF EXISTS uq_vrf_fabric_rd;
ALTER TABLE vrfs DROP COLUMN IF EXISTS rd;
ALTER TABLE vrfs ADD COLUMN IF NOT EXISTS route_target VARCHAR(32);

-- +goose StatementBegin
        DO $$ BEGIN
            CREATE TYPE bgp_address_family AS ENUM ('vpnv4','vpnv6','evpn');
        EXCEPTION WHEN duplicate_object THEN NULL;
        END $$;
-- +goose StatementEnd

        CREATE TABLE IF NOT EXISTS vrf_bgp_peers (
            id UUID PRIMARY KEY,
            vrf_id UUID NOT NULL REFERENCES vrfs(id),
            bgp_peer_id UUID NOT NULL REFERENCES bgp_peers(id),
            address_family bgp_address_family NOT NULL,
            rd VARCHAR(32),
            enabled BOOLEAN NOT NULL DEFAULT TRUE,
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT uq_vrf_bgp_peer_af UNIQUE (vrf_id, bgp_peer_id, address_family)
        );
CREATE INDEX IF NOT EXISTS ix_vrf_bgp_peers_vrf ON vrf_bgp_peers (vrf_id);
CREATE INDEX IF NOT EXISTS ix_vrf_bgp_peers_peer ON vrf_bgp_peers (bgp_peer_id);

-- +goose Down
DROP TABLE IF EXISTS vrf_bgp_peers;
DROP TYPE IF EXISTS bgp_address_family;
ALTER TABLE vrfs DROP COLUMN IF EXISTS route_target;
ALTER TABLE vrfs ADD COLUMN IF NOT EXISTS rd VARCHAR(32);
ALTER TABLE vrfs ADD CONSTRAINT uq_vrf_fabric_rd UNIQUE (fabric_id, rd);
