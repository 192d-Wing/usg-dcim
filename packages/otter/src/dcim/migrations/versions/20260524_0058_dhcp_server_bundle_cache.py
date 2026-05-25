"""Per-server bundle cache for the dhcp-site puller (PR 83).

PR 76 ships GET /dhcp/servers/{id}/bundle that assembles the Kea
config on every request. For sites with many scopes the assembly is
cheap but not free, and the dhcp-site puller polls on a timer —
every poll re-runs the assembler even when nothing changed.

This migration adds three columns:

  * bundle_cache_at   — when the cache was last refreshed
  * bundle_cache_etag — sha256 over the cached bundle (mirrors what
                        render_kea_bundle would compute live)
  * bundle_cache_json — the assembled {ctrl_agent, dhcp4, dhcp6} dict

A worker task (rerender_dhcp_bundle) writes these columns; the
scope-mutation and template-update handlers enqueue the task after
commit. The bundle endpoint reads from the cache when present and
falls back to live render when null (first request after install /
migration, before any mutation has fired a re-render).

Cache freshness is "best effort" — the endpoint trusts what's there
and doesn't check whether scopes have changed since bundle_cache_at.
If a mutation completed but its enqueued re-render hasn't run yet,
the next bundle pull serves slightly stale data; the puller polls
again in ~60s and gets the fresh etag.
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260524_0058"
down_revision: str | None = "20260524_0057"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        "ALTER TABLE dhcp_servers "
        "ADD COLUMN IF NOT EXISTS bundle_cache_at TIMESTAMPTZ"
    )
    op.execute(
        "ALTER TABLE dhcp_servers "
        "ADD COLUMN IF NOT EXISTS bundle_cache_etag VARCHAR(128)"
    )
    op.execute(
        "ALTER TABLE dhcp_servers "
        "ADD COLUMN IF NOT EXISTS bundle_cache_json JSONB"
    )


def downgrade() -> None:
    op.execute("ALTER TABLE dhcp_servers DROP COLUMN IF EXISTS bundle_cache_json")
    op.execute("ALTER TABLE dhcp_servers DROP COLUMN IF EXISTS bundle_cache_etag")
    op.execute("ALTER TABLE dhcp_servers DROP COLUMN IF EXISTS bundle_cache_at")
