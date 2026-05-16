"""OIDC role mapping — optional static scope binding.

Adds two columns to `oidc_role_mappings`:
  * scope_dimension — uses the existing scope_type pg enum (region,
    site, site_group, enclave, organization). NULL means global.
  * scope_target    — the target value for that dimension. UUIDs are
    accepted as strings; for site/region/site_group we resolve by
    `code` (e.g. "EUCOM"); for enclave/organization the value is the
    literal string already stored on Site.

When both columns are NULL the mapping continues to grant global
scope (matches the pre-migration behavior). When set, the mapped
DCIM role's capabilities are constrained to that scope target on
every login.

Revision ID: 20260512_0033
Revises: 20260512_0032
Create Date: 2026-05-12
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260512_0033"
down_revision: str | None = "20260512_0032"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        "ALTER TABLE oidc_role_mappings "
        "ADD COLUMN scope_dimension scope_type"
    )
    op.execute(
        "ALTER TABLE oidc_role_mappings "
        "ADD COLUMN scope_target VARCHAR(255)"
    )


def downgrade() -> None:
    op.execute("ALTER TABLE oidc_role_mappings DROP COLUMN IF EXISTS scope_target")
    op.execute("ALTER TABLE oidc_role_mappings DROP COLUMN IF EXISTS scope_dimension")
