"""Notification channels.

Outbound delivery targets for the alert engine. Routing today is a single
severity floor per channel; per-rule routing can join through this table
later without breaking the schema.

Revision ID: 20260509_0006
Revises: 20260508_0005
Create Date: 2026-05-09
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260509_0006"
down_revision: str | None = "20260508_0005"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    # The alert_severity enum already exists from the alerts table.
    op.execute(
        """
        DO $$ BEGIN
            CREATE TYPE channel_kind AS ENUM ('webhook','slack','email');
        EXCEPTION WHEN duplicate_object THEN NULL;
        END $$;
        """
    )
    op.execute(
        """
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
        """
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_notification_channels_kind "
        "ON notification_channels (kind);"
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_notification_channels_enabled "
        "ON notification_channels (enabled);"
    )


def downgrade() -> None:
    op.execute("DROP TABLE IF EXISTS notification_channels")
    op.execute("DROP TYPE IF EXISTS channel_kind")
