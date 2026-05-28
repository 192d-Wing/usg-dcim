"""Capability catalog + role bundle tests for the LIR module.

The LIR module's HTTP handlers and business logic live in
packages/otter-go/internal/lir/, but the capability catalog stays in
Python (per the otter→otter-go migration plan, Alembic + the cap
catalog are shared sources of truth until the migration completes).
These tests pin the `lir` domain wiring and role-bundle additions so
a future refactor of capabilities.py doesn't silently drop a code
the Go handlers depend on.
"""

from __future__ import annotations

import pytest

from dcim.security.capabilities import (
    BUILT_IN_ROLES,
    CAPABILITY_CATALOG,
    all_granular_codes,
)


def test_lir_domain_present_in_catalog():
    assert "lir" in CAPABILITY_CATALOG
    resources = CAPABILITY_CATALOG["lir"]
    assert set(resources.keys()) == {"pools", "requests", "allocations"}


@pytest.mark.parametrize("code", [
    "lir:pools:create", "lir:pools:read",
    "lir:pools:update", "lir:pools:delete",
    "lir:requests:create", "lir:requests:read",
    "lir:requests:cancel",
    "lir:requests:approve", "lir:requests:reject",
    "lir:allocations:read",
    "lir:allocations:return-request",
    "lir:allocations:return-confirm",
    "lir:allocations:arin-retry",
])
def test_lir_codes_emitted_by_all_granular_codes(code):
    assert code in all_granular_codes()


def test_regional_admin_gets_lir_star():
    """RegionalAdmin holds ipam:* and inventory:*; LIR is a sibling
    domain that should pick up the same wildcard treatment."""
    assert "lir:*" in BUILT_IN_ROLES["RegionalAdmin"]


@pytest.mark.parametrize("role", ["Viewer", "Auditor"])
def test_read_only_roles_get_lir_read(role):
    assert "lir:*:read" in BUILT_IN_ROLES[role]


def test_lir_nic_operator_bundle_exists():
    """Workflow role for the DoW NIC team running the LIR approval
    queue. Mirrors PowerOperator's shape (workflow-scoped, narrow
    elsewhere): lir:* plus the read caps needed for context."""
    nic = BUILT_IN_ROLES["LirNicOperator"]
    assert "lir:*" in nic
    assert "inventory:organizations:read" in nic
    assert "inventory:sites:read" in nic
    assert "ipam:supernets:read" in nic


def test_enterprise_admin_wildcard_still_covers_lir():
    """A safety pin — EnterpriseAdmin's `*` short-circuits cap matching
    in find_matching_capability, so any lir:* code must resolve under
    it. Catches a regression if someone narrows the wildcard."""
    assert BUILT_IN_ROLES["EnterpriseAdmin"] == ["*"]
