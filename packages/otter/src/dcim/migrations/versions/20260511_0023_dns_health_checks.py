"""Health-checked records (failover routing).

dns_health_checks per fabric — tcp / http / https / icmp probes that
the worker runs every interval_seconds. A DnsRecord can bind to one
check via the new health_check_id FK; when the check goes unhealthy,
the renderer drops the record from the rendered zone (and the bundle
etag flips so resolvers refresh).

Revision ID: 20260511_0023
Revises: 20260511_0022
Create Date: 2026-05-11
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260511_0023"
down_revision: str | None = "20260511_0022"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        "CREATE TYPE dns_health_check_protocol AS ENUM "
        "('tcp', 'http', 'https', 'icmp')"
    )
    op.execute(
        "CREATE TYPE dns_health_check_status AS ENUM "
        "('unknown', 'healthy', 'unhealthy')"
    )
    op.execute(
        """
        CREATE TABLE dns_health_checks (
            id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
            created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            name              VARCHAR(128) NOT NULL,
            fabric_id         UUID NOT NULL REFERENCES fabrics(id) ON DELETE CASCADE,
            target_ip         INET NOT NULL,
            protocol          dns_health_check_protocol NOT NULL,
            port              INTEGER,
            path              VARCHAR(255) NOT NULL DEFAULT '/',
            interval_seconds  INTEGER NOT NULL DEFAULT 30,
            timeout_seconds   INTEGER NOT NULL DEFAULT 5,
            enabled           BOOLEAN NOT NULL DEFAULT TRUE,
            status            dns_health_check_status NOT NULL DEFAULT 'unknown',
            last_checked_at   TIMESTAMPTZ,
            last_error        VARCHAR(512)
        )
        """
    )
    op.execute(
        "CREATE INDEX ix_dns_health_checks_fabric "
        "ON dns_health_checks (fabric_id)"
    )
    op.execute(
        "ALTER TABLE dns_records ADD COLUMN health_check_id UUID "
        "REFERENCES dns_health_checks(id) ON DELETE SET NULL"
    )
    op.execute(
        "CREATE INDEX ix_dns_records_health_check "
        "ON dns_records (health_check_id)"
    )


def downgrade() -> None:
    op.execute("DROP INDEX IF EXISTS ix_dns_records_health_check")
    op.execute("ALTER TABLE dns_records DROP COLUMN IF EXISTS health_check_id")
    op.execute("DROP TABLE IF EXISTS dns_health_checks")
    op.execute("DROP TYPE IF EXISTS dns_health_check_status")
    op.execute("DROP TYPE IF EXISTS dns_health_check_protocol")
