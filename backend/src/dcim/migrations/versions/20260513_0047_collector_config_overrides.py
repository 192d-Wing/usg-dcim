"""Per-collector runtime config overrides.

Adds collectors.config_overrides — a JSONB blob carrying optional ticker
intervals the collector applies on its next heartbeat tick. Lets an
operator slow / speed an individual collector's loops from the UI
without editing collector.yaml on the host.

Recognised keys (all ints, all seconds, all optional):
  - dns_metrics_interval_seconds  → DNS-agent Prom scrape cadence
  - device_poll_interval_seconds  → per-Device poll cadence (overrides YAML)
  - heartbeat_interval_seconds    → heartbeat tick itself

Empty {} means "use whatever the YAML says" — the collector's existing
defaults stay authoritative. The heartbeat response now echoes this
JSON so the Go collector picks it up on the next tick (max one
heartbeat interval of propagation lag).
"""

from collections.abc import Sequence

import sqlalchemy as sa
from alembic import op
from sqlalchemy.dialects import postgresql

revision: str = "20260513_0047"
down_revision: str | None = "20260513_0046"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.add_column(
        "collectors",
        sa.Column(
            "config_overrides",
            postgresql.JSONB(astext_type=sa.Text()),
            nullable=False,
            server_default=sa.text("'{}'::jsonb"),
        ),
    )


def downgrade() -> None:
    op.drop_column("collectors", "config_overrides")
