"""Seed OidcRoleMapping: Keycloak dcim-admin → EnterpriseAdmin (global scope).

Uses gen_random_uuid() + ON CONFLICT so the migration is safe to run on a
fresh DB (roles seeded by a prior migration) or a DB where the row already
exists from an earlier manual insert.
"""

from __future__ import annotations

import sqlalchemy as sa
from alembic import op

revision = "20260513_0043"
down_revision = "20260513_0042"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.execute(sa.text("""
        INSERT INTO oidc_role_mappings
            (id, idp_role, dcim_role_id,
             scope_dimension, scope_target, claim_source)
        SELECT gen_random_uuid(), 'dcim-admin', id,
               NULL, NULL, 'keycloak'
        FROM roles WHERE name = 'EnterpriseAdmin'
        ON CONFLICT (idp_role) DO NOTHING
    """))


def downgrade() -> None:
    op.execute(sa.text(
        "DELETE FROM oidc_role_mappings WHERE idp_role = 'dcim-admin'"
    ))
