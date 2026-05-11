"""ZSK rotation policy on dns_zones.

Adds zsk_rotation_days — when > 0, the worker rotates that zone's
ZSK every N days via dns_rotate_zsks. 0 (default) means the operator
rotates by hand from the DNSSEC tab.

DNSSEC at-rest encryption (Fernet on DnsKey.private_pem) is handled
in the service layer with a column-content prefix, so no schema
change is needed for that half.

Revision ID: 20260511_0025
Revises: 20260511_0024
Create Date: 2026-05-11
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260511_0025"
down_revision: str | None = "20260511_0024"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        "ALTER TABLE dns_zones ADD COLUMN zsk_rotation_days INTEGER "
        "NOT NULL DEFAULT 0"
    )


def downgrade() -> None:
    op.execute("ALTER TABLE dns_zones DROP COLUMN IF EXISTS zsk_rotation_days")
