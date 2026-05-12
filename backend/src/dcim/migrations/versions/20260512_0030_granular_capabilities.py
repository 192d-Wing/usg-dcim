"""Granular IAM-style capabilities — RBAC Phase 0 foundation.

Adds the `legacy_permission_codes` snapshot column to `roles` so the
pre-remap state is recoverable for one release cycle, then:

  - rewrites every system role's `permission_codes` from the new
    BUILT_IN_ROLES bundles (wildcards: EnterpriseAdmin = ['*'], etc.)
  - expands every non-system role's `permission_codes` by adding the
    granular codes each legacy 2-segment code implies, preserving the
    legacy codes themselves so any not-yet-tightened endpoint gates
    keep passing.

A follow-up migration in a later release will drop the snapshot column.

The legacy-code expansion table lives in this file rather than in
capabilities.py so the canonical capability module isn't carrying a
one-shot historical artifact. On a fresh install this migration runs
against an empty `roles` table and is a no-op; the seed_demo script
then creates roles directly from the new BUILT_IN_ROLES bundles.

Revision ID: 20260512_0030
Revises: 20260512_0029
Create Date: 2026-05-12
"""

from collections.abc import Sequence

import json

from alembic import op
from sqlalchemy import text

from dcim.security.capabilities import BUILT_IN_ROLES

revision: str = "20260512_0030"
down_revision: str | None = "20260512_0029"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


# --- Legacy -> granular expansion ----------------------------------------
# Local to this migration. Each entry says "if a role had this legacy
# code, it should also gain these granular ones." The legacy code is
# kept too, so any not-yet-tightened gate continues to pass for that
# role until subsequent phases tighten it.

_INV_RESOURCES = (
    "sites", "regions", "buildings", "rooms", "rows",
    "racks", "assets", "stencils",
)


def _crud(domain: str, *resources: str, actions: tuple[str, ...] = ("create", "read", "update", "delete")) -> list[str]:
    return [f"{domain}:{r}:{a}" for r in resources for a in actions]


LEGACY_CODE_EXPANSION: dict[str, list[str]] = {
    "inventory:read": _crud("inventory", *_INV_RESOURCES, actions=("read",)),
    "inventory:write": _crud("inventory", *_INV_RESOURCES, actions=("create", "update", "delete")),
    "inventory:bulk": ["inventory:bulk:execute"],
    "collector:read": ["collectors:collectors:read"],
    "collector:write": [
        "collectors:collectors:create",
        "collectors:collectors:update",
        "collectors:collectors:delete",
    ],
    "collector:enroll": ["collectors:collectors:enroll"],
    "collector:ingest": ["collectors:ingest:write"],
    "telemetry:read": ["telemetry:metrics:read", "telemetry:events:read"],
    "dashboard:read": ["dashboards:dashboards:read"],
    "alerts:read": ["alerts:alerts:read", "alerts:rules:read", "alerts:silences:read"],
    "alerts:ack": ["alerts:alerts:ack"],
    "alerts:configure": _crud("alerts", "rules", "silences", actions=("create", "update", "delete")),
    "power:control": ["power:control"],
    "power:approve": ["power:approve"],
    "audit:read": ["audit:events:read"],
    "users:manage": _crud("admin", "users"),
    "roles:manage": _crud("admin", "roles", "oidc-mappings"),
    "tokens:manage": _crud("admin", "api-tokens"),
    "sites:manage": _crud("inventory", "sites", actions=("create", "update", "delete")),
}


def _expand(codes: list[str]) -> list[str]:
    """Return `codes` deduped, plus every granular code each legacy
    code implies. Originals preserved at the front so any un-tightened
    gate keeps working."""
    seen: set[str] = set()
    out: list[str] = []
    for c in codes:
        if c not in seen:
            seen.add(c)
            out.append(c)
    for c in codes:
        for granular in LEGACY_CODE_EXPANSION.get(c, []):
            if granular not in seen:
                seen.add(granular)
                out.append(granular)
    return out


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
            existing = perms if isinstance(perms, list) else json.loads(perms or "[]")
            new_codes = _expand(existing)
        bind.execute(
            text("UPDATE roles SET permission_codes = CAST(:codes AS JSON) WHERE id = :id"),
            {"codes": json.dumps(new_codes), "id": role_id},
        )


def downgrade() -> None:
    op.execute(
        "UPDATE roles SET permission_codes = legacy_permission_codes "
        "WHERE legacy_permission_codes IS NOT NULL"
    )
    op.execute("ALTER TABLE roles DROP COLUMN IF EXISTS legacy_permission_codes")
