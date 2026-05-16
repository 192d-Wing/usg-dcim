"""Per-server top-queried-names on dns_server_metrics_samples.

Adds a JSONB column that the collector populates with the top-K
(name, type) -> count entries it observed during the interval. The
dashboard endpoint aggregates these across servers + samples in the
selected window to render the "Top queried names" widget.

The data source is dnstap on the resolver pod — CoreDNS's prometheus
plugin only emits per-zone counts (deliberate cardinality cap), so
per-name aggregation is collector-side. This column is nullable to
keep the migration a no-op for existing samples and for collectors
that haven't grown the dnstap reader yet.

Shape: `[{"name": "...", "type": "A", "count": 42}, ...]`. K is set
by the collector (default 100); the dashboard truncates further.

Revision ID: 20260512_0032
Revises: 20260512_0031
Create Date: 2026-05-12
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260512_0032"
down_revision: str | None = "20260512_0031"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        "ALTER TABLE dns_server_metrics_samples ADD COLUMN top_names JSONB"
    )


def downgrade() -> None:
    op.execute(
        "ALTER TABLE dns_server_metrics_samples DROP COLUMN IF EXISTS top_names"
    )
