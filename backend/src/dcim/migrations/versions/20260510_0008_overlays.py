"""Supernet hierarchy + VXLAN/GENEVE overlay tracking.

Adds:
  - supernets.parent_supernet_id (self-FK) and supernets.site_id so
    operators can model 10.0.0.0/8 → 10.0.0.0/20 (site/role aggregate) →
    10.0.0.0/24 (allocatable subnet).
  - overlays / vnis / vteps / vtep_vni_memberships for VXLAN-EVPN tracking.
  - subnets.vni_id pointing at the L2 VNI a tenant subnet rides on.

Each DDL statement gets its own op.execute() because asyncpg refuses
multi-statement prepared SQL.

Revision ID: 20260510_0008
Revises: 20260510_0007
Create Date: 2026-05-10
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260510_0008"
down_revision: str | None = "20260510_0007"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    # Enums first.
    op.execute(
        """
        DO $$ BEGIN
            CREATE TYPE overlay_kind AS ENUM ('vxlan','geneve');
        EXCEPTION WHEN duplicate_object THEN NULL;
        END $$;
        """
    )
    op.execute(
        """
        DO $$ BEGIN
            CREATE TYPE vni_kind AS ENUM ('l2','l3');
        EXCEPTION WHEN duplicate_object THEN NULL;
        END $$;
        """
    )
    op.execute(
        """
        DO $$ BEGIN
            CREATE TYPE vtep_role AS ENUM ('leaf','spine','border','other');
        EXCEPTION WHEN duplicate_object THEN NULL;
        END $$;
        """
    )

    # Supernet hierarchy: parent_supernet_id + site_id.
    op.execute(
        "ALTER TABLE supernets ADD COLUMN IF NOT EXISTS parent_supernet_id UUID "
        "REFERENCES supernets(id)"
    )
    op.execute(
        "ALTER TABLE supernets ADD COLUMN IF NOT EXISTS site_id UUID REFERENCES sites(id)"
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_supernets_parent ON supernets (parent_supernet_id)"
    )

    # Overlays.
    op.execute(
        """
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
        )
        """
    )
    op.execute("CREATE INDEX IF NOT EXISTS ix_overlays_fabric ON overlays (fabric_id)")

    # VNIs.
    op.execute(
        """
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
        )
        """
    )
    op.execute("CREATE INDEX IF NOT EXISTS ix_vnis_overlay ON vnis (overlay_id)")
    op.execute("CREATE INDEX IF NOT EXISTS ix_vnis_vrf ON vnis (vrf_id)")

    # VTEPs.
    op.execute(
        """
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
        )
        """
    )
    op.execute("CREATE INDEX IF NOT EXISTS ix_vteps_overlay ON vteps (overlay_id)")
    op.execute("CREATE INDEX IF NOT EXISTS ix_vteps_asset ON vteps (asset_id)")

    # VTEP ↔ VNI memberships.
    op.execute(
        """
        CREATE TABLE IF NOT EXISTS vtep_vni_memberships (
            id UUID PRIMARY KEY,
            vtep_id UUID NOT NULL REFERENCES vteps(id),
            vni_id UUID NOT NULL REFERENCES vnis(id),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT uq_vtep_vni_membership UNIQUE (vtep_id, vni_id)
        )
        """
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_vtep_vni_memberships_vtep "
        "ON vtep_vni_memberships (vtep_id)"
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_vtep_vni_memberships_vni "
        "ON vtep_vni_memberships (vni_id)"
    )

    # Subnet → L2 VNI binding.
    op.execute(
        "ALTER TABLE subnets ADD COLUMN IF NOT EXISTS vni_id UUID REFERENCES vnis(id)"
    )
    op.execute("CREATE INDEX IF NOT EXISTS ix_subnets_vni ON subnets (vni_id)")


def downgrade() -> None:
    op.execute("DROP INDEX IF EXISTS ix_subnets_vni")
    op.execute("ALTER TABLE subnets DROP COLUMN IF EXISTS vni_id")
    op.execute("DROP TABLE IF EXISTS vtep_vni_memberships")
    op.execute("DROP TABLE IF EXISTS vteps")
    op.execute("DROP TABLE IF EXISTS vnis")
    op.execute("DROP TABLE IF EXISTS overlays")
    op.execute("DROP INDEX IF EXISTS ix_supernets_parent")
    op.execute("ALTER TABLE supernets DROP COLUMN IF EXISTS site_id")
    op.execute("ALTER TABLE supernets DROP COLUMN IF EXISTS parent_supernet_id")
    op.execute("DROP TYPE IF EXISTS vtep_role")
    op.execute("DROP TYPE IF EXISTS vni_kind")
    op.execute("DROP TYPE IF EXISTS overlay_kind")
