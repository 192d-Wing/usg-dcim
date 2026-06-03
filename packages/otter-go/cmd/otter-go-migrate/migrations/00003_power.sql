-- +goose Up
-- +goose StatementBegin
DO $$ BEGIN CREATE TYPE asset_face AS ENUM ('front', 'rear');
                  EXCEPTION WHEN duplicate_object THEN NULL; END $$;
-- +goose StatementEnd
-- +goose StatementBegin
DO $$ BEGIN CREATE TYPE asset_mount AS ENUM ('rack', 'vertical-left', 'vertical-right');
                  EXCEPTION WHEN duplicate_object THEN NULL; END $$;
-- +goose StatementEnd
-- +goose StatementBegin
DO $$ BEGIN CREATE TYPE pdu_side AS ENUM ('A', 'B', 'C');
                  EXCEPTION WHEN duplicate_object THEN NULL; END $$;
-- +goose StatementEnd

        ALTER TABLE assets
            ADD COLUMN IF NOT EXISTS face asset_face NOT NULL DEFAULT 'front',
            ADD COLUMN IF NOT EXISTS mount asset_mount NOT NULL DEFAULT 'rack',
            ADD COLUMN IF NOT EXISTS pdu_side pdu_side,
            ADD COLUMN IF NOT EXISTS psu_count INTEGER;

        CREATE TABLE IF NOT EXISTS outlets (
            id           UUID PRIMARY KEY,
            pdu_asset_id UUID NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
            position     INTEGER NOT NULL,
            label        VARCHAR(32),
            phase        pdu_side,
            max_amps     INTEGER,
            receptacle   VARCHAR(16),
            created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
            updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
            CONSTRAINT uq_outlet_pdu_position UNIQUE (pdu_asset_id, position)
        );
CREATE INDEX IF NOT EXISTS ix_outlets_pdu ON outlets (pdu_asset_id);

        CREATE TABLE IF NOT EXISTS power_connections (
            id            UUID PRIMARY KEY,
            outlet_id     UUID NOT NULL REFERENCES outlets(id) ON DELETE CASCADE,
            asset_id      UUID NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
            psu_index     INTEGER NOT NULL DEFAULT 1,
            cord_color    VARCHAR(16),
            cord_length_m FLOAT,
            created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
            updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
            CONSTRAINT uq_power_connection_outlet UNIQUE (outlet_id)
        );
CREATE INDEX IF NOT EXISTS ix_power_connections_asset_psu ON power_connections (asset_id, psu_index);

-- +goose Down
DROP TABLE IF EXISTS power_connections CASCADE;
DROP TABLE IF EXISTS outlets CASCADE;

        ALTER TABLE assets
            DROP COLUMN IF EXISTS psu_count,
            DROP COLUMN IF EXISTS pdu_side,
            DROP COLUMN IF EXISTS mount,
            DROP COLUMN IF EXISTS face;
DROP TYPE IF EXISTS pdu_side;
DROP TYPE IF EXISTS asset_mount;
DROP TYPE IF EXISTS asset_face;
