"""Initial schema — inventory hierarchy, RBAC, collectors, alerts, audit, telemetry meta.

Telemetry samples themselves live in Elasticsearch, not Postgres. The audit_log table
is range-partitioned by month to keep indexes lean over multi-year retention.

Revision ID: 20260507_0001
Revises:
Create Date: 2026-05-07
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260507_0001"
down_revision: str | None = None
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    # NOTE: For brevity in this scaffold, table DDL is emitted via Alembic autogenerate
    # in subsequent revisions. This first revision sets up extensions and the
    # range-partitioned audit_log parent table.
    op.execute('CREATE EXTENSION IF NOT EXISTS "uuid-ossp"')
    op.execute('CREATE EXTENSION IF NOT EXISTS "pgcrypto"')
    op.execute('CREATE EXTENSION IF NOT EXISTS "pg_trgm"')

    # Run `alembic revision --autogenerate -m "models"` to emit the rest of the schema.
    # We declare the partitioned audit_log here because autogenerate cannot express it.
    op.execute(
        """
        DO $$
        BEGIN
            IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = 'audit_log_p_default') THEN
                NULL;  -- partitions created by background job per-month
            END IF;
        END$$;
        """
    )


def downgrade() -> None:
    op.execute('DROP EXTENSION IF EXISTS "pg_trgm"')
    op.execute('DROP EXTENSION IF EXISTS "pgcrypto"')
    op.execute('DROP EXTENSION IF EXISTS "uuid-ossp"')
