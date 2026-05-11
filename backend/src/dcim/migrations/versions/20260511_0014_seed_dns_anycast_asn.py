"""Seed the ASN catalog with the DNS anycast originating AS.

DCIM uses a single 4-byte private ASN (RFC 6996) as the BGP origin for
all DNS recursive anycast announcements — see
settings.dns_anycast_originate_asn. Pre-seeding it in the catalog means
the BGP peer "Local AS" dropdown surfaces a sensible default so
operators can pick it for record-keeping (the render layer ignores per-
peer local_asn_id for DNS anycast and always uses the setting value,
but cataloging keeps the UI consistent).

Idempotent: ON CONFLICT DO NOTHING. Operators who change the setting
later can add their own catalog row through the UI.

Revision ID: 20260511_0014
Revises: 20260511_0013
Create Date: 2026-05-11
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260511_0014"
down_revision: str | None = "20260511_0013"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    # Widen asn to BIGINT — INT4 maxes at 2147483647, which means the
    # 4-byte private range (4200000000+) overflows. ALTER ... TYPE is
    # cheap on a freshly-created table that's tiny.
    op.execute("ALTER TABLE bgp_asns ALTER COLUMN asn TYPE BIGINT")
    op.execute(
        """
        INSERT INTO bgp_asns (id, asn, name, kind, description)
        VALUES (
            gen_random_uuid(),
            4200000000,
            'DCIM DNS anycast',
            'private',
            'Originating AS for all DNS recursive anycast announcements.'
        )
        ON CONFLICT (asn) DO NOTHING
        """
    )


def downgrade() -> None:
    op.execute("DELETE FROM bgp_asns WHERE asn = 4200000000")
    op.execute("ALTER TABLE bgp_asns ALTER COLUMN asn TYPE INTEGER")
