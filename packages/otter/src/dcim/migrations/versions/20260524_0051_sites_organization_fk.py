"""Promote sites.organization (free-form string) to a real FK.

PR 66 — Phase 4 multi-tenancy. `sites.organization` is `VARCHAR(128)`
today; the `organizations` table exists with a proper UUID primary
key and rich per-org metadata (legal name, ARIN org id, address,
POCs). This migration adds `sites.organization_id` as a NULLABLE FK
on `organizations(id)` and backfills it by string-matching the
existing `sites.organization` value against `organizations.name`.

Design choices that bound this migration's risk:

- **Nullable FK, conservative backfill.** Many `sites.organization`
  values may not have a matching `organizations.name` row yet
  (organizations carries lots of NOT NULL columns — addresses, POCs
  — that we can't reasonably default for an auto-promoted org).
  Rows that don't match keep `organization_id = NULL`; operators
  use the query in `docs/DEPLOYMENT.md` to identify the gap and
  create the missing org rows on their own.
- **Keep the string column.** `sites.organization` stays in place as
  the legacy free-form tag; a follow-up PR will retire it once API
  consumers have migrated to the FK. ABAC continues to scope on the
  string today (see auth.ScopedSiteFilter — organization is one of
  several string dimensions); that path doesn't change here.
- **Unique index on `organizations.name`.** Required for the
  backfill JOIN to be deterministic. If the existing table already
  has duplicates the index creation will fail fast and the
  migration aborts — operators deduplicate manually and rerun.

For PR 66 the migration is data-only; no Go/Python handler code
changes ride along. That keeps the PR boundary small and reversible
and lets the API layer migrate to the FK in a separate, focused PR.
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260524_0051"
down_revision: str | None = "20260524_0050"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    # 1. Unique index on organizations.name. Required for the JOIN
    #    below to be deterministic. Fails the whole migration if
    #    duplicates exist — operators deduplicate manually.
    op.execute(
        "CREATE UNIQUE INDEX IF NOT EXISTS uq_organizations_name "
        "ON organizations (name)"
    )

    # 2. New nullable FK column on sites. No constraint yet — we
    #    populate the column first so the constraint validation
    #    succeeds in one shot (vs ADD NOT VALID + VALIDATE later).
    op.execute(
        "ALTER TABLE sites ADD COLUMN IF NOT EXISTS organization_id UUID"
    )

    # 3. Backfill from the legacy string column. Only sets the FK
    #    where sites.organization has an exact match against
    #    organizations.name. Unmatched rows stay NULL.
    op.execute(
        """
        UPDATE sites s
        SET    organization_id = o.id
        FROM   organizations o
        WHERE  s.organization IS NOT NULL
          AND  s.organization = o.name
          AND  s.organization_id IS NULL
        """
    )

    # 4. Add the FK constraint. Validates inline — fine on a small
    #    table; if sites ever grows large, prefer ADD CONSTRAINT
    #    NOT VALID + a separate VALIDATE CONSTRAINT pass to avoid
    #    locking writers.
    op.execute(
        "ALTER TABLE sites ADD CONSTRAINT fk_sites_organization_id "
        "FOREIGN KEY (organization_id) REFERENCES organizations(id)"
    )

    # 5. Index for FK-side filtering (LIST queries, future ABAC).
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_sites_organization_id "
        "ON sites (organization_id)"
    )


def downgrade() -> None:
    # Reverse in opposite order. IF EXISTS keeps the downgrade
    # idempotent for partial-failure recovery.
    op.execute("DROP INDEX IF EXISTS ix_sites_organization_id")
    op.execute(
        "ALTER TABLE sites DROP CONSTRAINT IF EXISTS fk_sites_organization_id"
    )
    op.execute("ALTER TABLE sites DROP COLUMN IF EXISTS organization_id")
    op.execute("DROP INDEX IF EXISTS uq_organizations_name")
