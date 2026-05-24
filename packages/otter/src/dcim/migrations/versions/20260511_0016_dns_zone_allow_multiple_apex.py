"""Drop the apex-per-fabric uniqueness rule.

A fabric can now hold multiple apex zones — useful when a single
fabric needs to serve more than one root domain (e.g. an internal
.mil zone alongside a tenant-specific apex). FQDN uniqueness via
uq_dns_zone_name remains the only constraint on dns_zones names.

Revision ID: 20260511_0016
Revises: 20260511_0015
Create Date: 2026-05-11
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260511_0016"
down_revision: str | None = "20260511_0015"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute("DROP INDEX IF EXISTS uq_dns_zone_one_apex_per_fabric")


def downgrade() -> None:
    op.execute(
        "CREATE UNIQUE INDEX uq_dns_zone_one_apex_per_fabric "
        "ON dns_zones (fabric_id) WHERE kind = 'apex'"
    )
