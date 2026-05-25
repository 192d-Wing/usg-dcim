"""Retire the legacy sites.organization string column (PR 92).

PR 66 added sites.organization_id (FK → organizations.id) and
backfilled it where the legacy string matched an organizations.name.
PR 67/68 wired the FK through otter-go + Python schemas. PR 69
pivoted ABAC scope to read the FK first, falling back to the string.
PR 90 wired the FK as a site-reachable scope dimension.

This migration drops the now-redundant string column. Operators who
still have role_scopes / oidc_role_mappings rows binding by org-name
must reassign them to the corresponding organizations.id UUID before
upgrade — the legacy string-matching path in security/scope.py is
removed in this same PR; un-migrated bindings become inert.

Forward-only safe: every API consumer (otter, otter-go, finch) now
reads organization_id; nothing in the codebase still writes to
sites.organization. Pre-flight check operators should run before
applying:

  SELECT s.id, s.code, s.name, s.organization AS legacy
  FROM sites s
  WHERE s.organization IS NOT NULL AND s.organization_id IS NULL;

Empty result = safe to drop. Non-empty = create the missing org
rows + UPDATE the sites.organization_id FK before applying this
migration, or the data is permanently lost.
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260524_0061"
down_revision: str | None = "20260524_0060"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    # Index from PR 7 was on sites.organization; gone with the column.
    op.execute("DROP INDEX IF EXISTS ix_sites_organization")
    op.execute("ALTER TABLE sites DROP COLUMN IF EXISTS organization")


def downgrade() -> None:
    # Re-add the column nullable so existing rows don't violate.
    # Operators recover the legacy string by joining sites.organization_id
    # → organizations.name and copying the result back (a follow-up
    # script, not part of this migration).
    op.execute("ALTER TABLE sites ADD COLUMN IF NOT EXISTS organization VARCHAR(128)")
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_sites_organization ON sites (organization)"
    )
