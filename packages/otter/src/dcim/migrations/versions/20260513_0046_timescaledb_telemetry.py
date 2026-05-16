"""TimescaleDB hypertable for telemetry samples.

Replaces the Elasticsearch-backed telemetry store with a Postgres-native
hypertable. Float time series on inverted indices cost ~50-80 bytes/doc in
ES versus ~4-8 bytes/sample under TimescaleDB columnar compression after the
7-day delay, and the freshness UPSERT can now share a transaction with the
sample INSERT.

Schema notes:
- The dedup constraint MUST include `ts` because TimescaleDB requires the
  hypertable time dimension to appear in every unique constraint. The
  idempotency semantic is unchanged: a retried batch lands on the same
  (collector_id, batch_id, seq, ts) tuple and ON CONFLICT DO NOTHING.
- `telemetry_hourly` is a continuous aggregate. It refreshes hourly with a
  3-hour lookback / 1-hour end-offset, which means alerts on rules with
  duration_seconds <= 1h still query the hypertable directly; the rollup
  serves dashboard reads and the forecast service.
- Compression segments by (site_id, asset_id, metric) so the index_scan +
  decompress path stays cheap for the (asset, metric, time-range) read.
- Retention is 24 months; older chunks are dropped automatically.

Operator prerequisite — `shared_preload_libraries`:
- TimescaleDB MUST be loaded via `shared_preload_libraries = 'timescaledb'`
  in postgresql.conf before this migration runs. The official
  `timescale/timescaledb` Docker image sets this automatically on a fresh
  data directory, but NOT when the image is pointed at a pgdata volume
  that was originally initialized by stock Postgres. In that case the
  CREATE EXTENSION call here dies mid-statement with
  "connection was closed in the middle of operation".
- Recovery on an upgraded volume: run
  `ALTER SYSTEM SET shared_preload_libraries = 'timescaledb';`
  against the cluster, restart Postgres, then re-run `alembic upgrade head`.

Revision ID: 20260513_0046
Revises: 20260513_0045
Create Date: 2026-05-13
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260513_0046"
down_revision: str | None = "20260513_0045"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute("CREATE EXTENSION IF NOT EXISTS timescaledb CASCADE")

    op.execute(
        """
        CREATE TABLE telemetry_samples (
            ts           TIMESTAMPTZ      NOT NULL,
            site_id      UUID             NOT NULL REFERENCES sites(id),
            asset_id     UUID             NOT NULL REFERENCES assets(id),
            collector_id UUID             NOT NULL REFERENCES collectors(id),
            batch_id     TEXT             NOT NULL,
            seq          INTEGER          NOT NULL,
            metric       TEXT             NOT NULL,
            value        DOUBLE PRECISION NOT NULL,
            unit         TEXT,
            received_at  TIMESTAMPTZ      NOT NULL,
            tags         JSONB            NOT NULL DEFAULT '{}'
        )
        """
    )

    op.execute(
        "ALTER TABLE telemetry_samples "
        "ADD CONSTRAINT uq_telem_sample_dedup "
        "UNIQUE (collector_id, batch_id, seq, ts)"
    )

    op.execute(
        "SELECT create_hypertable('telemetry_samples', 'ts', "
        "chunk_time_interval => INTERVAL '1 month')"
    )

    op.execute(
        "CREATE INDEX ix_telem_samples_asset_metric "
        "ON telemetry_samples (asset_id, metric, ts DESC)"
    )
    op.execute(
        "CREATE INDEX ix_telem_samples_site_metric "
        "ON telemetry_samples (site_id, metric, ts DESC)"
    )
    op.execute(
        "CREATE INDEX ix_telem_samples_tags "
        "ON telemetry_samples USING GIN (tags)"
    )

    op.execute(
        """
        ALTER TABLE telemetry_samples SET (
            timescaledb.compress,
            timescaledb.compress_segmentby = 'site_id, asset_id, metric',
            timescaledb.compress_orderby   = 'ts ASC'
        )
        """
    )
    op.execute("SELECT add_compression_policy('telemetry_samples', INTERVAL '7 days')")
    op.execute("SELECT add_retention_policy('telemetry_samples', INTERVAL '24 months')")

    op.execute(
        """
        CREATE MATERIALIZED VIEW telemetry_hourly
        WITH (timescaledb.continuous) AS
        SELECT
            time_bucket('1 hour', ts) AS bucket,
            site_id, asset_id, metric,
            AVG(value) AS avg_value,
            MAX(value) AS max_value,
            MIN(value) AS min_value,
            COUNT(*)   AS sample_count
        FROM telemetry_samples
        GROUP BY bucket, site_id, asset_id, metric
        WITH NO DATA
        """
    )
    op.execute(
        "SELECT add_continuous_aggregate_policy('telemetry_hourly', "
        "start_offset => INTERVAL '3 hours', "
        "end_offset => INTERVAL '1 hour', "
        "schedule_interval => INTERVAL '1 hour')"
    )


def downgrade() -> None:
    op.execute("SELECT remove_continuous_aggregate_policy('telemetry_hourly', if_exists => TRUE)")
    op.execute("DROP MATERIALIZED VIEW IF EXISTS telemetry_hourly")
    op.execute("SELECT remove_retention_policy('telemetry_samples', if_exists => TRUE)")
    op.execute("SELECT remove_compression_policy('telemetry_samples', if_exists => TRUE)")
    op.execute("DROP TABLE IF EXISTS telemetry_samples")
    # Leave the extension installed — other future tables may rely on it,
    # and dropping is rarely what you want on a downgrade.
