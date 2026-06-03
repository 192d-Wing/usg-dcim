-- +goose Up

-- +goose StatementBegin
        DO $$ BEGIN
            CREATE TYPE overlay_kind AS ENUM ('vxlan','geneve');
        EXCEPTION WHEN duplicate_object THEN NULL;
        END $$;
-- +goose StatementEnd

-- +goose StatementBegin
        DO $$ BEGIN
            CREATE TYPE vni_kind AS ENUM ('l2','l3');
        EXCEPTION WHEN duplicate_object THEN NULL;
        END $$;
-- +goose StatementEnd

-- +goose StatementBegin
        DO $$ BEGIN
            CREATE TYPE vtep_role AS ENUM ('leaf','spine','border','other');
        EXCEPTION WHEN duplicate_object THEN NULL;
        END $$;
-- +goose StatementEnd
ALTER TABLE supernets ADD COLUMN IF NOT EXISTS parent_supernet_id UUID REFERENCES supernets(id);
ALTER TABLE supernets ADD COLUMN IF NOT EXISTS site_id UUID REFERENCES sites(id);
CREATE INDEX IF NOT EXISTS ix_supernets_parent ON supernets (parent_supernet_id);

        CREATE TABLE IF NOT EXISTS overlays (
            id UUID PRIMARY KEY,
            fabric_id UUID NOT NULL REFERENCES fabrics(id),
            name VARCHAR(128) NOT NULL,
            kind overlay_kind NOT NULL DEFAULT 'vxlan',
            udp_port INTEGER NOT NULL DEFAULT 4789,
            mtu INTEGER,
            underlay_vrf_id UUID REFERENCES vrfs(id),
            description VARCHAR(512),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT uq_overlay_fabric_name UNIQUE (fabric_id, name)
        );
CREATE INDEX IF NOT EXISTS ix_overlays_fabric ON overlays (fabric_id);

        CREATE TABLE IF NOT EXISTS vnis (
            id UUID PRIMARY KEY,
            overlay_id UUID NOT NULL REFERENCES overlays(id),
            vni INTEGER NOT NULL,
            kind vni_kind NOT NULL DEFAULT 'l2',
            name VARCHAR(128),
            description VARCHAR(512),
            vlan_id INTEGER,
            evpn_route_target VARCHAR(64),
            vrf_id UUID REFERENCES vrfs(id),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT uq_vni_overlay_vni UNIQUE (overlay_id, vni)
        );
CREATE INDEX IF NOT EXISTS ix_vnis_overlay ON vnis (overlay_id);
CREATE INDEX IF NOT EXISTS ix_vnis_vrf ON vnis (vrf_id);

        CREATE TABLE IF NOT EXISTS vteps (
            id UUID PRIMARY KEY,
            overlay_id UUID NOT NULL REFERENCES overlays(id),
            asset_id UUID NOT NULL REFERENCES assets(id),
            loopback_ip INET,
            role vtep_role NOT NULL DEFAULT 'leaf',
            description VARCHAR(512),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT uq_vtep_overlay_asset UNIQUE (overlay_id, asset_id)
        );
CREATE INDEX IF NOT EXISTS ix_vteps_overlay ON vteps (overlay_id);
CREATE INDEX IF NOT EXISTS ix_vteps_asset ON vteps (asset_id);

        CREATE TABLE IF NOT EXISTS vtep_vni_memberships (
            id UUID PRIMARY KEY,
            vtep_id UUID NOT NULL REFERENCES vteps(id),
            vni_id UUID NOT NULL REFERENCES vnis(id),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT uq_vtep_vni_membership UNIQUE (vtep_id, vni_id)
        );
CREATE INDEX IF NOT EXISTS ix_vtep_vni_memberships_vtep ON vtep_vni_memberships (vtep_id);
CREATE INDEX IF NOT EXISTS ix_vtep_vni_memberships_vni ON vtep_vni_memberships (vni_id);
ALTER TABLE subnets ADD COLUMN IF NOT EXISTS vni_id UUID REFERENCES vnis(id);
CREATE INDEX IF NOT EXISTS ix_subnets_vni ON subnets (vni_id);

-- +goose Down
DROP INDEX IF EXISTS ix_subnets_vni;
ALTER TABLE subnets DROP COLUMN IF EXISTS vni_id;
DROP TABLE IF EXISTS vtep_vni_memberships;
DROP TABLE IF EXISTS vteps;
DROP TABLE IF EXISTS vnis;
DROP TABLE IF EXISTS overlays;
DROP INDEX IF EXISTS ix_supernets_parent;
ALTER TABLE supernets DROP COLUMN IF EXISTS site_id;
ALTER TABLE supernets DROP COLUMN IF EXISTS parent_supernet_id;
DROP TYPE IF EXISTS vtep_role;
DROP TYPE IF EXISTS vni_kind;
DROP TYPE IF EXISTS overlay_kind;
