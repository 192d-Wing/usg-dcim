"""Relax the dns_zones (fabric, site, kind) unique to apex-only.

The original constraint enforced both "one apex per fabric" and "one
site-zone per (fabric, site)". The second half is too strict — a site
can legitimately host multiple sub-zones (e.g. internal.site42.x and
lab.site42.x), and uniqueness of the zone *name* (uq_dns_zone_name)
already prevents true duplicates. Split into a partial unique index
that only enforces the apex rule.

Revision ID: 20260511_0015
Revises: 20260511_0014
Create Date: 2026-05-11
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260511_0015"
down_revision: str | None = "20260511_0014"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute("ALTER TABLE dns_zones DROP CONSTRAINT uq_dns_zone_fabric_site_kind")
    # Partial unique index — one apex zone per fabric. Sites can have
    # any number of site-kind zones (uq_dns_zone_name still prevents
    # duplicate FQDNs).
    op.execute(
        "CREATE UNIQUE INDEX uq_dns_zone_one_apex_per_fabric "
        "ON dns_zones (fabric_id) WHERE kind = 'apex'"
    )


def downgrade() -> None:
    op.execute("DROP INDEX IF EXISTS uq_dns_zone_one_apex_per_fabric")
    op.execute(
        "ALTER TABLE dns_zones ADD CONSTRAINT uq_dns_zone_fabric_site_kind "
        "UNIQUE (fabric_id, site_id, kind)"
    )
