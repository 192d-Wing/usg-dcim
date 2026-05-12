"""Granular IAM-style capabilities — Phase 0 foundation.

Adds the `legacy_permission_codes` snapshot column to `roles` so the
pre-remap state is recoverable for one release cycle, then:

  - rewrites every system role's `permission_codes` from the new
    BUILT_IN_ROLES bundles (wildcards: EnterpriseAdmin = ['*'], etc.)
  - expands every non-system role's `permission_codes` by adding the
    granular codes each legacy 2-segment code implies, preserving the
    legacy codes themselves so any not-yet-tightened endpoint gates
    keep passing.

A follow-up migration in a later release will drop the snapshot column.

Revision ID: 20260512_0030
Revises: 20260512_0029
Create Date: 2026-05-12
"""

from collections.abc import Sequence

import json

from alembic import op
from sqlalchemy import text

from dcim.security.capabilities import BUILT_IN_ROLES, expand_legacy

revision: str = "20260512_0030"
down_revision: str | None = "20260512_0029"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute("ALTER TABLE roles ADD COLUMN legacy_permission_codes JSON")
    op.execute(
        "UPDATE roles SET legacy_permission_codes = permission_codes "
        "WHERE legacy_permission_codes IS NULL"
    )

    bind = op.get_bind()
    rows = bind.execute(
        text("SELECT id, name, is_system, permission_codes FROM roles")
    ).fetchall()
    for role_id, name, is_system, perms in rows:
        if is_system and name in BUILT_IN_ROLES:
            new_codes = BUILT_IN_ROLES[name]
        else:
            # Non-system roles: expand legacy codes in place, keep the
            # originals so any un-tightened gate keeps working.
            existing = perms if isinstance(perms, list) else json.loads(perms or "[]")
            new_codes = expand_legacy(existing)
        bind.execute(
            text("UPDATE roles SET permission_codes = CAST(:codes AS JSON) WHERE id = :id"),
            {"codes": json.dumps(new_codes), "id": role_id},
        )


def downgrade() -> None:
    # Restore from snapshot, then drop the snapshot column.
    op.execute(
        "UPDATE roles SET permission_codes = legacy_permission_codes "
        "WHERE legacy_permission_codes IS NOT NULL"
    )
    op.execute("ALTER TABLE roles DROP COLUMN IF EXISTS legacy_permission_codes")
