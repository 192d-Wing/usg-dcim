"""Replace Vrf.rd with Vrf.route_target; add vrf_bgp_peers M:N.

The Route Distinguisher moves from the VRF row to a per-(VRF, peer, AF)
binding row in vrf_bgp_peers, since the same VRF can be advertised on
different BGP peers with different RDs and the same TCP session can
carry the VRF across multiple address families (VPNv4, VPNv6, EVPN).

Vrf.rd is dropped without preservation (operator chose "no migration"
when redesigning this surface). The accompanying unique constraint
uq_vrf_fabric_rd is also dropped.

Each DDL statement gets its own op.execute() because asyncpg refuses
multi-statement prepared SQL.

Revision ID: 20260511_0010
Revises: 20260510_0009
Create Date: 2026-05-11
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260511_0010"
down_revision: str | None = "20260510_0009"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    # Reshape vrfs: drop unique(fabric_id, rd), drop rd, add route_target.
    op.execute("ALTER TABLE vrfs DROP CONSTRAINT IF EXISTS uq_vrf_fabric_rd")
    op.execute("ALTER TABLE vrfs DROP COLUMN IF EXISTS rd")
    op.execute("ALTER TABLE vrfs ADD COLUMN IF NOT EXISTS route_target VARCHAR(32)")

    # BGP address family enum (used by vrf_bgp_peers.address_family).
    op.execute(
        """
        DO $$ BEGIN
            CREATE TYPE bgp_address_family AS ENUM ('vpnv4','vpnv6','evpn');
        EXCEPTION WHEN duplicate_object THEN NULL;
        END $$;
        """
    )

    # Association table: one row per (vrf, peer, AF) tuple. Carries the
    # RD specific to that binding. The DNS subsystem's bgp_peers table
    # is the FK target (created in migration 0009).
    op.execute(
        """
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
        )
        """
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_vrf_bgp_peers_vrf "
        "ON vrf_bgp_peers (vrf_id)"
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_vrf_bgp_peers_peer "
        "ON vrf_bgp_peers (bgp_peer_id)"
    )


def downgrade() -> None:
    op.execute("DROP TABLE IF EXISTS vrf_bgp_peers")
    op.execute("DROP TYPE IF EXISTS bgp_address_family")
    op.execute("ALTER TABLE vrfs DROP COLUMN IF EXISTS route_target")
    op.execute("ALTER TABLE vrfs ADD COLUMN IF NOT EXISTS rd VARCHAR(32)")
    op.execute(
        "ALTER TABLE vrfs ADD CONSTRAINT uq_vrf_fabric_rd "
        "UNIQUE (fabric_id, rd)"
    )
