"""Per-server CoreDNS metrics samples.

One row per scrape interval — collector polls CoreDNS's Prometheus
endpoint on :9153, diffs against the previous scrape, and POSTs the
delta back to central. The UI charts a recent window from this table.

Revision ID: 20260511_0021
Revises: 20260511_0020
Create Date: 2026-05-11
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260511_0021"
down_revision: str | None = "20260511_0020"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        """
        CREATE TABLE dns_server_metrics_samples (
            id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
            created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            server_id         UUID NOT NULL REFERENCES dns_servers(id) ON DELETE CASCADE,
            observed_at       TIMESTAMPTZ NOT NULL,
            interval_seconds  INTEGER NOT NULL,
            queries           BIGINT NOT NULL DEFAULT 0,
            nxdomain          BIGINT NOT NULL DEFAULT 0,
            servfail          BIGINT NOT NULL DEFAULT 0,
            noerror           BIGINT NOT NULL DEFAULT 0,
            p50_ms            DOUBLE PRECISION,
            p95_ms            DOUBLE PRECISION
        )
        """
    )
    op.execute(
        "CREATE INDEX ix_dns_metrics_server_observed "
        "ON dns_server_metrics_samples (server_id, observed_at)"
    )


def downgrade() -> None:
    op.execute("DROP TABLE IF EXISTS dns_server_metrics_samples")
