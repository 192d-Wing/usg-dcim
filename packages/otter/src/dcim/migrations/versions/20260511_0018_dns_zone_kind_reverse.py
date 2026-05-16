"""Add `reverse` to dns_zone_kind for IPv4/IPv6 reverse zones.

Reverse zones are auto-created by the IPAM projector at the /24 (v4)
or /64 (v6) boundary derived from IPAddress rows that have dns_name
set. They share the (fabric, site) scoping of forward site zones.

Revision ID: 20260511_0018
Revises: 20260511_0017
Create Date: 2026-05-11
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260511_0018"
down_revision: str | None = "20260511_0017"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    # ALTER TYPE ... ADD VALUE must run outside a transaction in older
    # Postgres, but PG 12+ supports it in-transaction so this works for
    # our 16+ target. The IF NOT EXISTS guard makes it idempotent.
    op.execute("ALTER TYPE dns_zone_kind ADD VALUE IF NOT EXISTS 'reverse'")


def downgrade() -> None:
    # Postgres doesn't support removing enum values without recreating
    # the type. Documented one-way migration — operators rolling back
    # accept the orphaned enum label.
    pass
