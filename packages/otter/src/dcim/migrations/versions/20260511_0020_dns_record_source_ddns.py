"""Add `ddns` to dns_record_source for DHCP-driven projections.

DHCP-sourced IPAddress rows produce A/AAAA/PTR records whose lifetime
tracks the lease — distinguishing them from static IPAM rows lets the
UI tell operators "this record will vanish when the lease ends."

Revision ID: 20260511_0020
Revises: 20260511_0019
Create Date: 2026-05-11
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260511_0020"
down_revision: str | None = "20260511_0019"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute("ALTER TYPE dns_record_source ADD VALUE IF NOT EXISTS 'ddns'")


def downgrade() -> None:
    # Postgres doesn't support DROP VALUE; one-way migration.
    pass
