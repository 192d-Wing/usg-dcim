"""Seed OidcRoleMapping: Keycloak dcim-admin → EnterpriseAdmin (global scope).

Without this row, users who authenticate via Keycloak with the dcim-admin
realm role receive zero DCIM capabilities — caps_from_idp_roles() finds no
matching oidc_role_mappings row and returns an empty dict.

The EnterpriseAdmin role carries ['*'] so this mapping grants full access,
mirroring what a locally-created admin user receives via UserRole.
"""

from __future__ import annotations

import uuid

from alembic import op
import sqlalchemy as sa

revision = "20260513_0043"
down_revision = "20260513_0042"
branch_labels = None
depends_on = None

_ENTERPRISE_ADMIN_ROLE_ID = "f8c95eb9-a708-46ca-a8ec-12f348059588"


def upgrade() -> None:
    op.execute(
        sa.text("""
            INSERT INTO oidc_role_mappings
                (id, idp_role, dcim_role_id, scope_dimension, scope_target, claim_source)
            VALUES
                (:id, 'dcim-admin', :role_id, NULL, NULL, 'keycloak')
            ON CONFLICT (idp_role) DO UPDATE
                SET dcim_role_id    = EXCLUDED.dcim_role_id,
                    scope_dimension = NULL,
                    scope_target    = NULL,
                    claim_source    = EXCLUDED.claim_source
        """).bindparams(
            id=str(uuid.uuid4()),
            role_id=_ENTERPRISE_ADMIN_ROLE_ID,
        )
    )


def downgrade() -> None:
    op.execute(
        sa.text("DELETE FROM oidc_role_mappings WHERE idp_role = 'dcim-admin'")
    )
