"""DNSSEC key material + signed-zone flag.

dns_keys carries the per-zone KSK + ZSK pair plus their PEM-encoded
private halves; dns_zones.signed flips when an operator runs the
enable-dnssec endpoint. Key rotation + encrypted-at-rest are deferred
to a follow-up.

Revision ID: 20260511_0024
Revises: 20260511_0023
Create Date: 2026-05-11
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260511_0024"
down_revision: str | None = "20260511_0023"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute("CREATE TYPE dns_key_role AS ENUM ('ksk', 'zsk')")
    op.execute(
        "CREATE TYPE dns_key_algorithm AS ENUM "
        "('ecdsap256sha256', 'ed25519', 'rsasha256')"
    )
    op.execute(
        """
        CREATE TABLE dns_keys (
            id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
            created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            zone_id         UUID NOT NULL REFERENCES dns_zones(id) ON DELETE CASCADE,
            role            dns_key_role NOT NULL,
            algorithm       dns_key_algorithm NOT NULL,
            private_pem     TEXT NOT NULL,
            public_key_b64  TEXT NOT NULL,
            key_tag         INTEGER NOT NULL,
            active_from     TIMESTAMPTZ NOT NULL,
            retired_at      TIMESTAMPTZ
        )
        """
    )
    op.execute("CREATE INDEX ix_dns_keys_zone ON dns_keys (zone_id)")
    op.execute(
        "ALTER TABLE dns_zones ADD COLUMN signed BOOLEAN NOT NULL DEFAULT FALSE"
    )


def downgrade() -> None:
    op.execute("ALTER TABLE dns_zones DROP COLUMN IF EXISTS signed")
    op.execute("DROP TABLE IF EXISTS dns_keys")
    op.execute("DROP TYPE IF EXISTS dns_key_algorithm")
    op.execute("DROP TYPE IF EXISTS dns_key_role")
