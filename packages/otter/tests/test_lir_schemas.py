"""Schema + capability-catalog tests for the LIR module (phase 1).

Pure: no DB, no async. Pins the validation rules on
LirPoolCreate / LirRequestCreate that mirror the DB CHECK constraints
in migration 20260528_0065, the capability catalog wiring for the
new `lir` domain, and the role-bundle additions (LirNicOperator + the
read grants on Viewer/Auditor + the `lir:*` grant on RegionalAdmin).

DB-level invariants (the supernet xor-CHECK, the request decision
consistency CHECK, the allocation status enums) are exercised by
the integration suite once the API endpoints land in later phases —
this file only covers what the Pydantic + capability layers own.
"""

from __future__ import annotations

from uuid import uuid4

import pytest
from pydantic import ValidationError

from dcim.models.lir import (
    LirAllocationStatus,
    LirArinStatus,
    LirRequestStatus,
)
from dcim.schemas.lir import (
    LirAllocationOut,
    LirPoolCreate,
    LirPoolUpdate,
    LirRequestApprove,
    LirRequestCreate,
    LirRequestReject,
)
from dcim.security.capabilities import (
    BUILT_IN_ROLES,
    CAPABILITY_CATALOG,
    all_granular_codes,
)

# ---------- LirPoolCreate validation ----------

def _v4_pool(**over) -> dict:
    base = {
        "name": "DoW IPv4 NIPR",
        "slug": "dow-v4-nipr",
        "ip_family": 4,
        "min_prefix_length": 20,
        "max_prefix_length": 29,
        "arin_parent_net_handle": "NET-198-51-100-0-1",
    }
    base.update(over)
    return base


def _v6_pool(**over) -> dict:
    base = {
        "name": "DoW IPv6 NIPR",
        "slug": "dow-v6-nipr",
        "ip_family": 6,
        "min_prefix_length": 32,
        "max_prefix_length": 56,
    }
    base.update(over)
    return base


def test_pool_v4_round_trip():
    p = LirPoolCreate(**_v4_pool())
    assert p.ip_family == 4
    assert p.enabled is True
    assert p.arin_parent_net_handle == "NET-198-51-100-0-1"


def test_pool_v6_no_arin_handle_is_allowed():
    """LIR-internal pools (no ARIN feed-up) leave the handle null."""
    p = LirPoolCreate(**_v6_pool())
    assert p.arin_parent_net_handle is None


def test_pool_rejects_min_above_max():
    with pytest.raises(ValidationError) as exc:
        LirPoolCreate(**_v4_pool(min_prefix_length=28, max_prefix_length=24))
    assert "min_prefix_length" in str(exc.value)


def test_pool_rejects_v4_prefix_over_32():
    with pytest.raises(ValidationError):
        LirPoolCreate(**_v4_pool(max_prefix_length=40))


def test_pool_rejects_v6_prefix_over_128():
    with pytest.raises(ValidationError):
        LirPoolCreate(**_v6_pool(max_prefix_length=130))


def test_pool_rejects_v6_prefix_over_family_cap_at_min():
    # min must also fit in the family — a v4 pool can't carve /40s
    # even at the lower bound.
    with pytest.raises(ValidationError):
        LirPoolCreate(**_v4_pool(min_prefix_length=33, max_prefix_length=33))


def test_pool_rejects_bad_family():
    with pytest.raises(ValidationError):
        LirPoolCreate(**_v4_pool(ip_family=5))


def test_pool_update_is_partial():
    """Update schema should accept just one field — used by PATCH."""
    u = LirPoolUpdate(enabled=False)
    assert u.enabled is False
    assert u.name is None


# ---------- LirRequestCreate validation ----------

def _request(**over) -> dict:
    base = {
        "organization_id": str(uuid4()),
        "ip_family": 4,
        "prefix_length": 28,
        "justification": "Need a /28 for the new lab segment at SITE-EX-01.",
    }
    base.update(over)
    return base


def test_request_round_trip():
    r = LirRequestCreate(**_request())
    assert r.prefix_length == 28
    assert r.pool_id is None
    assert r.site_id is None


def test_request_rejects_empty_justification():
    """Field has min_length=1 — empty string is rejected at the
    schema layer so the API surfaces a clean 422."""
    with pytest.raises(ValidationError):
        LirRequestCreate(**_request(justification=""))


def test_request_rejects_v4_prefix_over_32():
    with pytest.raises(ValidationError):
        LirRequestCreate(**_request(ip_family=4, prefix_length=40))


def test_request_rejects_v6_prefix_over_128():
    with pytest.raises(ValidationError):
        LirRequestCreate(**_request(ip_family=6, prefix_length=129))


def test_request_v6_accepts_64():
    r = LirRequestCreate(**_request(ip_family=6, prefix_length=64))
    assert r.ip_family == 6


def test_request_rejects_bad_family():
    with pytest.raises(ValidationError):
        LirRequestCreate(**_request(ip_family=5))


# ---------- Approve / reject body shape ----------

def test_approve_body_is_all_optional():
    """Approving with no overrides keeps the tenant's pool preference."""
    a = LirRequestApprove()
    assert a.approved_pool_id is None
    assert a.notes is None


def test_reject_requires_reason():
    with pytest.raises(ValidationError):
        LirRequestReject(reason="")


# ---------- Status enum round-trip ----------

def test_request_status_values():
    """The model's status column uses these literal strings — pinning
    them so a rename in the enum doesn't silently break the API
    contract or the migration's CHECK constraint."""
    assert LirRequestStatus.pending_approval.value == "pending_approval"
    assert LirRequestStatus.approved.value == "approved"
    assert LirRequestStatus.rejected.value == "rejected"
    assert LirRequestStatus.cancelled.value == "cancelled"
    assert LirRequestStatus.failed.value == "failed"


def test_allocation_status_values():
    assert LirAllocationStatus.active.value == "active"
    assert LirAllocationStatus.return_requested.value == "return_requested"
    assert LirAllocationStatus.returned.value == "returned"


def test_arin_status_values():
    assert LirArinStatus.none.value == "none"
    assert {s.value for s in LirArinStatus} == {
        "none", "pending", "registered", "failed", "removing", "removed",
    }


def test_allocation_out_accepts_enum_status():
    """LirAllocationOut declares Lir*Status — feed it the enum values
    a row would carry. Confirms the response model materializes
    cleanly without an enum coercion bug."""
    payload = {
        "id": uuid4(),
        "request_id": uuid4(),
        "organization_id": uuid4(),
        "pool_id": uuid4(),
        "pool_supernet_id": uuid4(),
        "tenant_supernet_id": uuid4(),
        "prefix": "10.0.0.0/28",
        "allocated_at": "2026-05-28T00:00:00+00:00",
        "allocated_by_user_id": uuid4(),
        "status": "active",
        "arin_status": "none",
        "arin_attempts": 0,
        "created_at": "2026-05-28T00:00:00+00:00",
        "updated_at": "2026-05-28T00:00:00+00:00",
    }
    out = LirAllocationOut(**payload)
    assert out.status is LirAllocationStatus.active
    assert out.arin_status is LirArinStatus.none


# ---------- Capability catalog wiring ----------

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


# ---------- Built-in role bundles ----------

def test_regional_admin_gets_lir_star():
    """RegionalAdmin already has ipam:* and inventory:*; LIR is a sibling
    domain so it should pick up the wildcard treatment."""
    assert "lir:*" in BUILT_IN_ROLES["RegionalAdmin"]


@pytest.mark.parametrize("role", ["Viewer", "Auditor"])
def test_read_only_roles_get_lir_read(role):
    assert "lir:*:read" in BUILT_IN_ROLES[role]


def test_lir_nic_operator_bundle_exists():
    """Workflow role for the DoW NIC team — mirrors PowerOperator's
    shape (workflow-scoped). lir:* plus the read caps needed for
    context (organizations, sites, supernets)."""
    nic = BUILT_IN_ROLES["LirNicOperator"]
    assert "lir:*" in nic
    assert "inventory:organizations:read" in nic
    assert "inventory:sites:read" in nic
    assert "ipam:supernets:read" in nic


def test_enterprise_admin_wildcard_still_covers_lir():
    """A safety pin — EnterpriseAdmin's `*` short-circuits cap matching
    in find_matching_capability, so any lir:* code must resolve under
    it. Catch a future regression where someone narrows the wildcard."""
    assert BUILT_IN_ROLES["EnterpriseAdmin"] == ["*"]
