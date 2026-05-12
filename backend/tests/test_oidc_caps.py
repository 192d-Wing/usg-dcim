"""Unit tests for the zero-trust OIDC capability resolver.

`_resolve_mapping_scope` and `caps_from_idp_roles` are called on every
authenticated request, so a bug here either over-grants (security) or
under-grants (operators see spurious 403s). The DB-touching branches
use a FakeSession stub — pulling in a real Postgres or aiosqlite for
these few queries is overkill given how shaped they are.
"""

from __future__ import annotations

from types import SimpleNamespace
from typing import Any
from uuid import UUID, uuid4

import pytest

from dcim.models.auth import ScopeType
from dcim.security.scope import (
    Scope,
    _resolve_mapping_scope,
    caps_from_idp_roles,
)


# ---------- minimal fake AsyncSession ----------

class _FakeScalars:
    def __init__(self, vals: list[Any]):
        self._vals = list(vals)

    def all(self) -> list[Any]:
        return self._vals


class _FakeResult:
    def __init__(self, val: Any):
        self._val = val

    def scalar_one_or_none(self) -> Any:
        return self._val

    def scalars(self) -> _FakeScalars:
        if self._val is None:
            return _FakeScalars([])
        if isinstance(self._val, list):
            return _FakeScalars(self._val)
        return _FakeScalars([self._val])


class FakeSession:
    """Stub `AsyncSession` for the two scope helpers.

    Queries are matched on the SELECT entity's `__tablename__`, looked
    up by table → registry. `get(Model, pk)` reads from `.gets`."""

    def __init__(self) -> None:
        # table_name -> {filter_value: result_id_or_obj}
        self.tables: dict[str, dict[Any, Any]] = {}
        # (Model, pk) -> obj
        self.gets: dict[tuple[Any, Any], Any] = {}

    async def execute(self, stmt: Any) -> _FakeResult:
        froms = list(stmt.get_final_froms())
        assert froms, f"stmt has no FROM: {stmt}"
        table_name = froms[0].name

        # Walk the where clause to find a literal we can key off. For
        # the queries we exercise here it's always a single equality
        # (`Model.code == 'foo'`) or an IN-list (`Model.idp_role IN (...)`).
        bound = self._extract_bind_values(stmt)

        registry = self.tables.get(table_name, {})
        if not bound:
            # No filter — return everything.
            return _FakeResult(list(registry.values()))

        if isinstance(bound, list):
            hits = [registry[v] for v in bound if v in registry]
            return _FakeResult(hits)
        return _FakeResult(registry.get(bound))

    async def get(self, model: Any, pk: Any) -> Any:
        return self.gets.get((model, pk))

    @staticmethod
    def _extract_bind_values(stmt: Any) -> Any:
        """Best-effort: pull the literal values out of the where clause.
        Returns a single value, a list (for IN), or None."""
        params = stmt.compile().params
        values = list(params.values())
        if not values:
            return None
        if len(values) == 1:
            v = values[0]
            return v
        return values  # treat multi-value as IN-list


# ---------- _resolve_mapping_scope ----------

async def test_resolve_global_when_dimension_none():
    out = await _resolve_mapping_scope(FakeSession(), None, None)
    assert out is not None and out.is_global


async def test_resolve_global_when_target_empty():
    out = await _resolve_mapping_scope(FakeSession(), ScopeType.region, "")
    assert out is not None and out.is_global


async def test_resolve_global_dimension_global_():
    out = await _resolve_mapping_scope(
        FakeSession(), ScopeType.global_, "anything",
    )
    assert out is not None and out.is_global


async def test_resolve_enclave_is_pure():
    """Enclave + organization are literals — no DB lookup, just wrap."""
    out = await _resolve_mapping_scope(FakeSession(), ScopeType.enclave, "il5")
    assert out == Scope(enclaves=frozenset(["il5"]))


async def test_resolve_organization_is_pure():
    out = await _resolve_mapping_scope(
        FakeSession(), ScopeType.organization, "usaf",
    )
    assert out == Scope(organizations=frozenset(["usaf"]))


async def test_resolve_region_hits_db():
    region_id = uuid4()
    db = FakeSession()
    db.tables["regions"] = {"us-east": region_id}

    out = await _resolve_mapping_scope(db, ScopeType.region, "us-east")
    assert out == Scope(region_ids=frozenset([region_id]))


async def test_resolve_region_unknown_returns_none():
    """Fail-closed: missing target → cap is NOT granted on this login."""
    db = FakeSession()
    db.tables["regions"] = {}
    out = await _resolve_mapping_scope(db, ScopeType.region, "atlantis")
    assert out is None


async def test_resolve_site_hits_db():
    site_id = uuid4()
    db = FakeSession()
    db.tables["sites"] = {"site-42": site_id}

    out = await _resolve_mapping_scope(db, ScopeType.site, "site-42")
    assert out == Scope(site_ids=frozenset([site_id]))


async def test_resolve_site_unknown_returns_none():
    db = FakeSession()
    db.tables["sites"] = {}
    out = await _resolve_mapping_scope(db, ScopeType.site, "ghost-site")
    assert out is None


async def test_resolve_site_group_keyed_by_name():
    """SiteGroup has no `code` column — matcher uses `name`."""
    group_id = uuid4()
    db = FakeSession()
    db.tables["site_groups"] = {"east-coast": group_id}

    out = await _resolve_mapping_scope(db, ScopeType.site_group, "east-coast")
    assert out == Scope(site_group_ids=frozenset([group_id]))


async def test_resolve_fabric_hits_db():
    fabric_id = uuid4()
    db = FakeSession()
    db.tables["fabrics"] = {"prod-fabric": fabric_id}

    out = await _resolve_mapping_scope(db, ScopeType.fabric, "prod-fabric")
    assert out == Scope(fabric_ids=frozenset([fabric_id]))


async def test_resolve_fabric_unknown_returns_none():
    db = FakeSession()
    db.tables["fabrics"] = {}
    out = await _resolve_mapping_scope(db, ScopeType.fabric, "missing")
    assert out is None


# ---------- caps_from_idp_roles ----------

def _mapping(
    *,
    idp_role: str,
    role_id: UUID,
    dim: ScopeType | None = None,
    target: str | None = None,
) -> Any:
    """Stand-in for the OidcRoleMapping row."""
    return SimpleNamespace(
        idp_role=idp_role,
        dcim_role_id=role_id,
        scope_dimension=dim,
        scope_target=target,
    )


def _role(*, perms: list[str]) -> Any:
    return SimpleNamespace(permission_codes=perms)


async def test_caps_from_idp_roles_empty_returns_empty():
    assert await caps_from_idp_roles(FakeSession(), []) == {}


async def test_caps_from_idp_roles_filters_non_strings():
    """Defensive: the JWT claim could ship a non-string member."""
    out = await caps_from_idp_roles(
        FakeSession(), [None, "", 42],  # type: ignore[list-item]
    )
    assert out == {}


async def test_caps_from_idp_roles_unmapped_returns_empty():
    db = FakeSession()
    db.tables["oidc_role_mappings"] = {}
    out = await caps_from_idp_roles(db, ["random-role"])
    assert out == {}


async def test_caps_from_idp_roles_grants_role_caps_globally():
    """Mapping with no scope binding → cap is granted globally."""
    from dcim.models.auth import Role

    role_id = uuid4()
    db = FakeSession()
    db.tables["oidc_role_mappings"] = {
        "dcim-admins": _mapping(idp_role="dcim-admins", role_id=role_id),
    }
    db.gets[(Role, role_id)] = _role(perms=["dns:zones:read", "dns:zones:create"])

    out = await caps_from_idp_roles(db, ["dcim-admins"])
    assert set(out) == {"dns:zones:read", "dns:zones:create"}
    for scope in out.values():
        assert scope.is_global


async def test_caps_from_idp_roles_constrains_by_scope_binding():
    """A mapping bound to (site, 'site-42') restricts the granted caps
    to that site instead of granting globally."""
    from dcim.models.auth import Role

    role_id = uuid4()
    site_id = uuid4()
    db = FakeSession()
    db.tables["oidc_role_mappings"] = {
        "dcim-site42-ops": _mapping(
            idp_role="dcim-site42-ops",
            role_id=role_id,
            dim=ScopeType.site,
            target="site-42",
        ),
    }
    db.tables["sites"] = {"site-42": site_id}
    db.gets[(Role, role_id)] = _role(perms=["dns:zones:read"])

    out = await caps_from_idp_roles(db, ["dcim-site42-ops"])
    assert "dns:zones:read" in out
    cap = out["dns:zones:read"]
    assert cap.is_global is False
    assert cap.site_ids == {site_id}


async def test_caps_from_idp_roles_skips_mapping_with_missing_target():
    """Fail-closed: mapping bound to a target the DB doesn't know about
    is silently skipped (the cap is NOT granted)."""
    from dcim.models.auth import Role

    role_id = uuid4()
    db = FakeSession()
    db.tables["oidc_role_mappings"] = {
        "dcim-ghost-ops": _mapping(
            idp_role="dcim-ghost-ops",
            role_id=role_id,
            dim=ScopeType.site,
            target="never-existed",
        ),
    }
    db.tables["sites"] = {}
    db.gets[(Role, role_id)] = _role(perms=["dns:zones:read"])

    out = await caps_from_idp_roles(db, ["dcim-ghost-ops"])
    assert out == {}


async def test_caps_from_idp_roles_skips_mapping_with_missing_role():
    """If the dcim_role_id FK is dangling (role was deleted), drop the
    mapping rather than crash. Pragmatic — admin UI prevents this case
    but defense in depth."""
    role_id = uuid4()
    db = FakeSession()
    db.tables["oidc_role_mappings"] = {
        "orphan": _mapping(idp_role="orphan", role_id=role_id),
    }
    # Note: no entry in db.gets — db.get() returns None.

    out = await caps_from_idp_roles(db, ["orphan"])
    assert out == {}


async def test_caps_from_idp_roles_unions_two_mappings_for_same_cap():
    """A user holding two IdP roles that both grant `dns:zones:read`,
    one at site-42 and one at site-99, should end up with a single
    cap entry whose site_ids = {42, 99}."""
    from dcim.models.auth import Role

    role_a = uuid4()
    role_b = uuid4()
    site_a = uuid4()
    site_b = uuid4()

    db = FakeSession()
    db.tables["oidc_role_mappings"] = {
        "ops-east": _mapping(
            idp_role="ops-east", role_id=role_a,
            dim=ScopeType.site, target="site-east",
        ),
        "ops-west": _mapping(
            idp_role="ops-west", role_id=role_b,
            dim=ScopeType.site, target="site-west",
        ),
    }
    db.tables["sites"] = {"site-east": site_a, "site-west": site_b}
    db.gets[(Role, role_a)] = _role(perms=["dns:zones:read"])
    db.gets[(Role, role_b)] = _role(perms=["dns:zones:read"])

    out = await caps_from_idp_roles(db, ["ops-east", "ops-west"])
    assert out["dns:zones:read"].site_ids == {site_a, site_b}
    assert out["dns:zones:read"].is_global is False


async def test_caps_from_idp_roles_global_mapping_dominates_scoped():
    """If one mapping grants `dns:zones:read` globally and another
    grants it at a site, the union promotes the cap to global (the
    operator can read any zone)."""
    from dcim.models.auth import Role

    role_a = uuid4()
    role_b = uuid4()
    site_id = uuid4()

    db = FakeSession()
    db.tables["oidc_role_mappings"] = {
        "global-admin": _mapping(idp_role="global-admin", role_id=role_a),
        "site-op": _mapping(
            idp_role="site-op", role_id=role_b,
            dim=ScopeType.site, target="site-1",
        ),
    }
    db.tables["sites"] = {"site-1": site_id}
    db.gets[(Role, role_a)] = _role(perms=["dns:zones:read"])
    db.gets[(Role, role_b)] = _role(perms=["dns:zones:read"])

    out = await caps_from_idp_roles(db, ["global-admin", "site-op"])
    assert out["dns:zones:read"].is_global is True


# pytest-asyncio's auto mode picks up `async def test_*` coroutines
# without an explicit @pytest.mark.asyncio. Pinned via pyproject.toml.
_ = pytest  # silence unused-import linter
