"""dns_server_metrics_samples: index observed_at for the retention purge.

Background: PR #208 introduces the Go scheduler harness with
dns_purge_metrics as its first job — hourly DELETE on
dns_server_metrics_samples WHERE observed_at < $cutoff. The existing
composite index (server_id, observed_at) from migration 0021 leads
with server_id; a predicate that doesn't constrain server_id can't
use it, so the DELETE would seq-scan a high-volume scrape table
every hour.

This migration adds:

    CREATE INDEX ix_dns_metrics_observed_at
    ON dns_server_metrics_samples (observed_at);

Single-column index serves the retention DELETE directly. Doesn't
duplicate the existing composite — the planner can still use
(server_id, observed_at) for per-server time-window queries
(ListDnsServerMetricsSamples).

For an append-mostly metrics table a BRIN index on observed_at would
be even cheaper to maintain, but BRIN's coarse-grained min/max per
range can over-include rows on out-of-order inserts (collector
backfills, mistimed clocks). The B-tree gives us crisp deletes for
the cost of ~20MB per million rows — acceptable for the scrape
volume we see today (~5M rows / month / server fleet).

Downgrade drops the index (no schema change).
"""
from __future__ import annotations

from alembic import op

revision = "20260531_0067"
down_revision = "20260529_0066"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.create_index(
        "ix_dns_metrics_observed_at",
        "dns_server_metrics_samples",
        ["observed_at"],
    )


def downgrade() -> None:
    op.drop_index(
        "ix_dns_metrics_observed_at",
        table_name="dns_server_metrics_samples",
    )
