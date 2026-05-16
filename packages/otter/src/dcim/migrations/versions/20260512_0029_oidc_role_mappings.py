"""OIDC role mappings.

New table `oidc_role_mappings` that lets admins map a role string
asserted by the IdP (Keycloak realm role, Okta/ADFS group, etc.) to a
DCIM Role. The OIDC callback consults this table on each sign-in to
attach the matching role(s) to the user.

Revision ID: 20260512_0029
Revises: 20260512_0028
Create Date: 2026-05-12
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260512_0029"
down_revision: str | None = "20260512_0028"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    # One DDL per execute() — asyncpg refuses multi-statement SQL.
    op.execute(
        """
        CREATE TABLE oidc_role_mappings (
            id UUID PRIMARY KEY,
            idp_role VARCHAR(255) NOT NULL,
            claim_source VARCHAR(64) NOT NULL DEFAULT 'keycloak',
            dcim_role_id UUID NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
            description VARCHAR(255),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT uq_oidc_role_mapping_idp_role UNIQUE (idp_role)
        )
        """
    )
    op.execute(
        "CREATE INDEX ix_oidc_role_mappings_dcim_role "
        "ON oidc_role_mappings (dcim_role_id)"
    )


def downgrade() -> None:
    op.execute("DROP INDEX IF EXISTS ix_oidc_role_mappings_dcim_role")
    op.execute("DROP TABLE IF EXISTS oidc_role_mappings")
