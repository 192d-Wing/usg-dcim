-- +goose Up

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
        );
CREATE INDEX ix_dns_metrics_server_observed ON dns_server_metrics_samples (server_id, observed_at);

-- +goose Down
DROP TABLE IF EXISTS dns_server_metrics_samples;
