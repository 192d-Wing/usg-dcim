"""Zero-trust OIDC capability sync — wipe OIDC-sourced UserRole rows.

Until this migration the OIDC callback path called `_sync_oidc_roles`,
which inserted `user_roles` rows for any IdP role that matched an
`oidc_role_mappings` entry. Those rows were never revoked when the IdP
role was removed from the user.

Zero-trust pivot: OIDC-derived capabilities are now materialized
per-request from the `idp_roles` claim in our session JWT, joined to
`oidc_role_mappings` at evaluation time. The `user_roles` table is
reserved for *manual* admin-attached assignments only.

This migration deletes every `user_roles` row whose user has
`sso_subject IS NOT NULL` (i.e. arrived via OIDC). RoleScope rows
attached to those assignments are also deleted. Local-auth users
(sso_subject IS NULL — e.g. admin@dcim.local) are untouched.

Edge case: an admin who manually attached a DCIM role to an OIDC
user via the Admin UI will lose that grant. Re-attach as needed.
This is documented as a known limitation; preserving it would
require a `source` column we deliberately didn't add (no clean way
to distinguish historically-sourced rows without one).

Revision ID: 20260512_0031
Revises: 20260512_0030
Create Date: 2026-05-12
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260512_0031"
down_revision: str | None = "20260512_0030"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        """
        DELETE FROM role_scopes
        WHERE assignment_id IN (
            SELECT ur.id FROM user_roles ur
            JOIN users u ON u.id = ur.user_id
            WHERE u.sso_subject IS NOT NULL
        )
        """
    )
    op.execute(
        """
        DELETE FROM user_roles
        WHERE user_id IN (SELECT id FROM users WHERE sso_subject IS NOT NULL)
        """
    )


def downgrade() -> None:
    # Irreversible — the wiped rows aren't snapshotted. Operators
    # who need them back must re-sign-in (after restoring the old
    # _sync_oidc_roles path) or re-attach manually.
    pass
