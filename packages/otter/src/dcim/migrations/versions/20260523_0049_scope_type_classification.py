"""Add 'classification' to the scope_type ENUM.

PR 61 introduces classification as an ABAC scope dimension alongside the
existing enclave / organization / site / region / site_group / fabric
dimensions. role_scopes rows with scope_type='classification' now flow
into Principal.Scopes[cap].Classifications and are enforced via
auth.EnforceClassification on the otter-go side.

Postgres ENUMs are append-only via ALTER TYPE ADD VALUE — this
migration adds the new value and the downgrade is intentionally a
no-op (Postgres does not support removing enum values without a full
type rebuild + dependent-column rewrite; doing so on a live audited
table is more risk than the migration would ever earn back).
"""

from collections.abc import Sequence

from alembic import op


revision: str = "20260523_0049"
down_revision: str | None = "20260515_0048"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    # ADD VALUE IF NOT EXISTS is idempotent — re-running the migration
    # on a database that already has the value is a no-op rather than
    # an error.
    op.execute("ALTER TYPE scope_type ADD VALUE IF NOT EXISTS 'classification'")


def downgrade() -> None:
    # Intentional no-op. Removing an enum value in Postgres requires
    # creating a new type, swapping every dependent column, and
    # rebuilding indexes. The downside of leaving the value behind on
    # downgrade is purely cosmetic.
    pass
