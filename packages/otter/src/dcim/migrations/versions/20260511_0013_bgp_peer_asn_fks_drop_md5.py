"""Reshape bgp_peers: ASN columns become FKs + drop MD5 password +
add TCP AO key chain FK.

Both AS columns (local_asn / peer_asn integers) drop in favor of FKs
into the ASN catalog (bgp_asns). MD5 password is removed entirely —
RFC 5925 TCP AO superseded it, and the new tcp_ao_key_chain_id FK
replaces the inline secret with a reference to a rotatable key chain.

The migration is destructive: existing bgp_peers rows must be deleted
before this runs because the new FK columns are NOT NULL and there's
no automatic mapping from "AS 65000" → "ASN catalog row". Operators
re-catalog ASNs (under BGP peers → ASNs) and recreate any peers after
upgrade.

Revision ID: 20260511_0013
Revises: 20260511_0012
Create Date: 2026-05-11
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260511_0013"
down_revision: str | None = "20260511_0012"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    # Wipe peers first — the dependent rows (anycast_bgp_bindings,
    # vrf_bgp_peers) cascade-protect, so clear them too.
    op.execute("DELETE FROM anycast_bgp_bindings")
    op.execute("DELETE FROM vrf_bgp_peers")
    op.execute("DELETE FROM bgp_peers")

    op.execute("ALTER TABLE bgp_peers DROP COLUMN IF EXISTS local_asn")
    op.execute("ALTER TABLE bgp_peers DROP COLUMN IF EXISTS peer_asn")
    op.execute("ALTER TABLE bgp_peers DROP COLUMN IF EXISTS md5_password")

    op.execute(
        "ALTER TABLE bgp_peers ADD COLUMN local_asn_id UUID NOT NULL "
        "REFERENCES bgp_asns(id)"
    )
    op.execute(
        "ALTER TABLE bgp_peers ADD COLUMN peer_asn_id UUID NOT NULL "
        "REFERENCES bgp_asns(id)"
    )
    op.execute(
        "ALTER TABLE bgp_peers ADD COLUMN tcp_ao_key_chain_id UUID "
        "REFERENCES tcp_ao_key_chains(id)"
    )


def downgrade() -> None:
    op.execute("DELETE FROM anycast_bgp_bindings")
    op.execute("DELETE FROM vrf_bgp_peers")
    op.execute("DELETE FROM bgp_peers")
    op.execute("ALTER TABLE bgp_peers DROP COLUMN IF EXISTS tcp_ao_key_chain_id")
    op.execute("ALTER TABLE bgp_peers DROP COLUMN IF EXISTS peer_asn_id")
    op.execute("ALTER TABLE bgp_peers DROP COLUMN IF EXISTS local_asn_id")
    op.execute("ALTER TABLE bgp_peers ADD COLUMN local_asn INTEGER NOT NULL DEFAULT 0")
    op.execute("ALTER TABLE bgp_peers ADD COLUMN peer_asn INTEGER NOT NULL DEFAULT 0")
    op.execute("ALTER TABLE bgp_peers ADD COLUMN md5_password VARCHAR(128)")
