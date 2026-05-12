"""User.idp_refresh_token — encrypted IdP refresh token storage.

The new POST /auth/refresh endpoint uses this to mint fresh session
JWTs without forcing the user through an interactive Keycloak round-
trip. Encryption uses Fernet keyed by `dns_dnssec_secret` (the same
setting that protects DnsKey.private_pem at rest); plaintext stored
when the setting is unset, matching the DnsKey precedent.

Revision ID: 20260512_0035
Revises: 20260512_0034
Create Date: 2026-05-12
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260512_0035"
down_revision: str | None = "20260512_0034"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute("ALTER TABLE users ADD COLUMN idp_refresh_token TEXT")
    op.execute("ALTER TABLE users ADD COLUMN idp_refresh_token_iat TIMESTAMPTZ")


def downgrade() -> None:
    op.execute("ALTER TABLE users DROP COLUMN IF EXISTS idp_refresh_token_iat")
    op.execute("ALTER TABLE users DROP COLUMN IF EXISTS idp_refresh_token")
