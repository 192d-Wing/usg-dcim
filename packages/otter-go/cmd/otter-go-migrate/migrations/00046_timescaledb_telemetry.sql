-- +goose Up
CREATE EXTENSION IF NOT EXISTS timescaledb CASCADE;

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
        );
ALTER TABLE telemetry_samples ADD CONSTRAINT uq_telem_sample_dedup UNIQUE (collector_id, batch_id, seq, ts);
SELECT create_hypertable('telemetry_samples', 'ts', chunk_time_interval => INTERVAL '1 month');
CREATE INDEX ix_telem_samples_asset_metric ON telemetry_samples (asset_id, metric, ts DESC);
CREATE INDEX ix_telem_samples_site_metric ON telemetry_samples (site_id, metric, ts DESC);
CREATE INDEX ix_telem_samples_tags ON telemetry_samples USING GIN (tags);

        ALTER TABLE telemetry_samples SET (
            timescaledb.compress,
            timescaledb.compress_segmentby = 'site_id, asset_id, metric',
            timescaledb.compress_orderby   = 'ts ASC'
        );
SELECT add_compression_policy('telemetry_samples', INTERVAL '7 days');
SELECT add_retention_policy('telemetry_samples', INTERVAL '24 months');

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
        WITH NO DATA;
SELECT add_continuous_aggregate_policy('telemetry_hourly', start_offset => INTERVAL '3 hours', end_offset => INTERVAL '1 hour', schedule_interval => INTERVAL '1 hour');

-- +goose Down
SELECT remove_continuous_aggregate_policy('telemetry_hourly', if_exists => TRUE);
DROP MATERIALIZED VIEW IF EXISTS telemetry_hourly;
SELECT remove_retention_policy('telemetry_samples', if_exists => TRUE);
SELECT remove_compression_policy('telemetry_samples', if_exists => TRUE);
DROP TABLE IF EXISTS telemetry_samples;
