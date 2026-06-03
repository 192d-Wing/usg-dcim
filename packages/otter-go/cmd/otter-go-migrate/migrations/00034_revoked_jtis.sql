-- +goose Up

        CREATE TABLE revoked_jtis (
            jti VARCHAR(64) PRIMARY KEY,
            user_id UUID REFERENCES users(id) ON DELETE CASCADE,
            revoked_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            reason VARCHAR(64),
            expires_at TIMESTAMPTZ NOT NULL
        );
CREATE INDEX ix_revoked_jtis_expires ON revoked_jtis (expires_at);

-- +goose Down
DROP INDEX IF EXISTS ix_revoked_jtis_expires;
DROP TABLE IF EXISTS revoked_jtis;
