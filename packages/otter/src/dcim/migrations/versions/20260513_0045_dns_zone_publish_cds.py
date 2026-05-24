"""Add publish_cds opt-out flag for RFC 7344 CDS/CDNSKEY emission.

When a zone is signed AND publish_cds=true, the renderer emits
CDNSKEY + CDS records at the zone apex so a parent zone scanner
(RFC 8078) can auto-update its DS records on KSK rotation.

Default TRUE because the records are harmless when the parent
does not scan — they just add a few bytes per zone. Operators
who want manual DS handoff (e.g. during a coordinated cross-
parent rotation) can flip this to FALSE per zone.

Revision ID: 20260513_0045
Revises: 20260513_0044
Create Date: 2026-05-13
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260513_0045"
down_revision: str | None = "20260513_0044"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        "ALTER TABLE dns_zones ADD COLUMN publish_cds BOOLEAN NOT NULL DEFAULT TRUE"
    )


def downgrade() -> None:
    op.execute(
        "ALTER TABLE dns_zones DROP COLUMN IF EXISTS publish_cds"
    )
