"""Per-fabric RFC 9432 catalog zone.

Adds `dns_catalog_zones` with one row per fabric. The catalog zone
lets external BIND / Knot / PowerDNS primaries auto-provision the
set of authoritative zones DCIM owns by AXFR-ing this catalog and
reading its member entries — the standard RFC 9432 producer flow.

Membership is computed at render time from the existing `dns_zones`
rows (apex + site + reverse, frozen elided) so there's no
membership table here; the join lives in the bundle assembly.

DNSSEC + TSIG for the catalog ride later phases:
  - Phase 2 (0042): TSIG keys + transfer ACL columns.
  - Phase 2 (same revision): DNSSEC signing piggybacks on the
    existing `dns_keys` table — no new column needed since the
    catalog itself is just another zone.

Revision ID: 20260513_0041
Revises: 20260512_0040
Create Date: 2026-05-13
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260513_0041"
down_revision: str | None = "20260512_0040"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    # One DDL per execute() — asyncpg refuses multi-statement SQL.
    op.execute(
        "CREATE TABLE dns_catalog_zones ("
        "  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),"
        "  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),"
        "  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),"
        "  fabric_id UUID NOT NULL"
        "    REFERENCES fabrics(id) ON DELETE CASCADE,"
        "  name VARCHAR(253) NOT NULL,"
        "  enabled BOOLEAN NOT NULL DEFAULT TRUE,"
        "  signed BOOLEAN NOT NULL DEFAULT FALSE"
        ")"
    )
    op.execute(
        "ALTER TABLE dns_catalog_zones "
        "ADD CONSTRAINT uq_dns_catalog_fabric UNIQUE (fabric_id)"
    )
    op.execute(
        "ALTER TABLE dns_catalog_zones "
        "ADD CONSTRAINT uq_dns_catalog_name UNIQUE (name)"
    )
    op.execute(
        "CREATE INDEX ix_dns_catalog_fabric "
        "ON dns_catalog_zones (fabric_id)"
    )


def downgrade() -> None:
    op.execute("DROP TABLE IF EXISTS dns_catalog_zones")
