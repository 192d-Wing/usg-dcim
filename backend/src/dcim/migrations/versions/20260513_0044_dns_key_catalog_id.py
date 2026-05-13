"""Add catalog_id FK to dns_keys for catalog-zone DNSSEC.

Extends the dns_keys table so signing keys can be attached to a
DnsCatalogZone (catalog_id) in addition to a DnsZone (zone_id). A
CHECK constraint enforces exactly one FK is non-null per row — the
two key scopes are mutually exclusive. An index on catalog_id
mirrors ix_dns_keys_zone for query parity.

Revision ID: 20260513_0044
Revises: 20260513_0043
Create Date: 2026-05-13
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260513_0044"
down_revision: str | None = "20260513_0043"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        "ALTER TABLE dns_keys ADD COLUMN catalog_id UUID "
        "REFERENCES dns_catalog_zones(id) ON DELETE CASCADE"
    )
    # Make zone_id nullable so catalog-scoped rows can leave it NULL.
    # The CHECK constraint below enforces the exactly-one-set invariant.
    op.execute(
        "ALTER TABLE dns_keys ALTER COLUMN zone_id DROP NOT NULL"
    )
    op.execute(
        "CREATE INDEX ix_dns_keys_catalog ON dns_keys(catalog_id)"
    )
    # Exactly one scope column must be set. This guards against
    # rows that are orphaned (neither zone nor catalog) or
    # double-scoped (both set, ambiguous ownership).
    op.execute(
        "ALTER TABLE dns_keys ADD CONSTRAINT ck_dns_keys_scope "
        "CHECK ((zone_id IS NOT NULL) != (catalog_id IS NOT NULL))"
    )


def downgrade() -> None:
    op.execute(
        "ALTER TABLE dns_keys DROP CONSTRAINT IF EXISTS ck_dns_keys_scope"
    )
    op.execute(
        "DROP INDEX IF EXISTS ix_dns_keys_catalog"
    )
    op.execute(
        "ALTER TABLE dns_keys ALTER COLUMN zone_id SET NOT NULL"
    )
    op.execute(
        "ALTER TABLE dns_keys DROP COLUMN IF EXISTS catalog_id"
    )
