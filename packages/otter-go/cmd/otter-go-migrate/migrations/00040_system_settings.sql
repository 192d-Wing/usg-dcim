-- +goose Up

        CREATE TABLE system_settings (
            key VARCHAR(64) PRIMARY KEY,
            value JSON,
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
        );

-- +goose Down
DROP TABLE IF EXISTS system_settings;
