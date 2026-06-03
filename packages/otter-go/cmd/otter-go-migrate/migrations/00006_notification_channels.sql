-- +goose Up

-- +goose StatementBegin
        DO $$ BEGIN
            CREATE TYPE channel_kind AS ENUM ('webhook','slack','email');
        EXCEPTION WHEN duplicate_object THEN NULL;
        END $$;
-- +goose StatementEnd

        CREATE TABLE IF NOT EXISTS notification_channels (
            id UUID PRIMARY KEY,
            name VARCHAR(128) NOT NULL UNIQUE,
            kind channel_kind NOT NULL,
            config_json JSONB NOT NULL DEFAULT '{}',
            min_severity alert_severity NOT NULL DEFAULT 'warning',
            notify_on_fire BOOLEAN NOT NULL DEFAULT TRUE,
            notify_on_resolve BOOLEAN NOT NULL DEFAULT TRUE,
            enabled BOOLEAN NOT NULL DEFAULT TRUE,
            description VARCHAR(512),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
        );
CREATE INDEX IF NOT EXISTS ix_notification_channels_kind ON notification_channels (kind);
CREATE INDEX IF NOT EXISTS ix_notification_channels_enabled ON notification_channels (enabled);

-- +goose Down
DROP TABLE IF EXISTS notification_channels;
DROP TYPE IF EXISTS channel_kind;
