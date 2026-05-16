"""Revoked-JTI table for session-JWT revocation.

A leaked session JWT used to be valid until its 15-min TTL expired —
no server-side way to invalidate. Now every issued JWT carries a `jti`
claim, and `revoked_jtis` is the deny-list checked on every
authenticated request.

  * jti        : the token's unique id (UUID4 stringified).
  * user_id    : the subject; lets us bulk-revoke a user's tokens.
  * revoked_at : timestamp the revocation took effect.
  * reason     : free-form (e.g. "user_logout", "admin_revoke",
                 "suspected_compromise"). Audit-only.
  * expires_at : when the JWT itself would have expired anyway.
                 The cleanup job evicts rows past this so the table
                 stays small.

Revision ID: 20260512_0034
Revises: 20260512_0033
Create Date: 2026-05-12
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260512_0034"
down_revision: str | None = "20260512_0033"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        """
        CREATE TABLE revoked_jtis (
            jti VARCHAR(64) PRIMARY KEY,
            user_id UUID REFERENCES users(id) ON DELETE CASCADE,
            revoked_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            reason VARCHAR(64),
            expires_at TIMESTAMPTZ NOT NULL
        )
        """
    )
    op.execute(
        "CREATE INDEX ix_revoked_jtis_expires "
        "ON revoked_jtis (expires_at)"
    )


def downgrade() -> None:
    op.execute("DROP INDEX IF EXISTS ix_revoked_jtis_expires")
    op.execute("DROP TABLE IF EXISTS revoked_jtis")
