-- +goose Up
CREATE TYPE dns_health_check_protocol AS ENUM ('tcp', 'http', 'https', 'icmp');
CREATE TYPE dns_health_check_status AS ENUM ('unknown', 'healthy', 'unhealthy');

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
        );
CREATE INDEX ix_dns_health_checks_fabric ON dns_health_checks (fabric_id);
ALTER TABLE dns_records ADD COLUMN health_check_id UUID REFERENCES dns_health_checks(id) ON DELETE SET NULL;
CREATE INDEX ix_dns_records_health_check ON dns_records (health_check_id);

-- +goose Down
DROP INDEX IF EXISTS ix_dns_records_health_check;
ALTER TABLE dns_records DROP COLUMN IF EXISTS health_check_id;
DROP TABLE IF EXISTS dns_health_checks;
DROP TYPE IF EXISTS dns_health_check_status;
DROP TYPE IF EXISTS dns_health_check_protocol;
