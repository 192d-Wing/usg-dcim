-- +goose Up

        CREATE TABLE dns_views (
            id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
            created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            name         VARCHAR(64) NOT NULL,
            fabric_id    UUID NOT NULL REFERENCES fabrics(id) ON DELETE CASCADE,
            match_cidrs  JSON NOT NULL DEFAULT '[]'::json,
            priority     INTEGER NOT NULL DEFAULT 100,
            description  VARCHAR(512),
            CONSTRAINT uq_dns_view_fabric_name UNIQUE (fabric_id, name)
        );
CREATE INDEX ix_dns_views_fabric ON dns_views (fabric_id);
ALTER TABLE dns_records ADD COLUMN view_id UUID REFERENCES dns_views(id) ON DELETE SET NULL;
CREATE INDEX ix_dns_records_view ON dns_records (view_id);

-- +goose Down
DROP INDEX IF EXISTS ix_dns_records_view;
ALTER TABLE dns_records DROP COLUMN IF EXISTS view_id;
DROP TABLE IF EXISTS dns_views;
