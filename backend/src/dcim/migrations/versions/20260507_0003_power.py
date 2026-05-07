"""Power chain: face/mount/pdu_side/psu_count on assets, outlets, power_connections.

Revision ID: 20260507_0003
Revises: 20260507_0002
Create Date: 2026-05-07
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260507_0003"
down_revision: str | None = "20260507_0002"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    # New enum types
    op.execute("""DO $$ BEGIN CREATE TYPE asset_face AS ENUM ('front', 'rear');
                  EXCEPTION WHEN duplicate_object THEN NULL; END $$;""")
    op.execute("""DO $$ BEGIN CREATE TYPE asset_mount AS ENUM ('rack', 'vertical-left', 'vertical-right');
                  EXCEPTION WHEN duplicate_object THEN NULL; END $$;""")
    op.execute("""DO $$ BEGIN CREATE TYPE pdu_side AS ENUM ('A', 'B', 'C');
                  EXCEPTION WHEN duplicate_object THEN NULL; END $$;""")

    # Asset columns
    op.execute("""
        ALTER TABLE assets
            ADD COLUMN IF NOT EXISTS face asset_face NOT NULL DEFAULT 'front',
            ADD COLUMN IF NOT EXISTS mount asset_mount NOT NULL DEFAULT 'rack',
            ADD COLUMN IF NOT EXISTS pdu_side pdu_side,
            ADD COLUMN IF NOT EXISTS psu_count INTEGER
    """)

    # Outlets — children of PDU assets
    op.execute("""
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
        )
    """)
    op.execute("CREATE INDEX IF NOT EXISTS ix_outlets_pdu ON outlets (pdu_asset_id)")

    # Power connections — outlet → device PSU
    op.execute("""
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
        )
    """)
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_power_connections_asset_psu "
        "ON power_connections (asset_id, psu_index)"
    )


def downgrade() -> None:
    op.execute("DROP TABLE IF EXISTS power_connections CASCADE")
    op.execute("DROP TABLE IF EXISTS outlets CASCADE")
    op.execute("""
        ALTER TABLE assets
            DROP COLUMN IF EXISTS psu_count,
            DROP COLUMN IF EXISTS pdu_side,
            DROP COLUMN IF EXISTS mount,
            DROP COLUMN IF EXISTS face
    """)
    op.execute("DROP TYPE IF EXISTS pdu_side")
    op.execute("DROP TYPE IF EXISTS asset_mount")
    op.execute("DROP TYPE IF EXISTS asset_face")
