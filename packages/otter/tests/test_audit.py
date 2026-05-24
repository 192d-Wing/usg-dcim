"""Unit tests for the audit recorder's value normalizer.

`record()` writes into a JSON column, so anything in the diff /
metadata payload has to coerce cleanly. Pydantic's
`model_dump(exclude_unset=True)` returns Python-native objects (UUID,
datetime, enum members) that the stdlib JSON encoder rejects — we test
the normalizer pins this down once so every PATCH endpoint stays safe
without per-call boilerplate.
"""

import enum
import json
from datetime import UTC, date, datetime
from uuid import UUID, uuid4

from dcim.security.audit import _json_safe


def test_json_safe_passes_through_primitives():
    assert _json_safe(None) is None
    assert _json_safe(True) is True
    assert _json_safe(42) == 42
    assert _json_safe("str") == "str"
    assert _json_safe(3.14) == 3.14


def test_json_safe_coerces_uuid_to_string():
    u = uuid4()
    out = _json_safe({"site_id": u})
    assert out == {"site_id": str(u)}
    # Round-trip through json.dumps to prove the dict is serializable now.
    json.dumps(out)


def test_json_safe_coerces_datetime_and_date_to_iso():
    dt = datetime(2026, 5, 10, 12, 34, 56, tzinfo=UTC)
    d = date(2026, 5, 10)
    out = _json_safe({"when": dt, "day": d})
    assert out == {"when": dt.isoformat(), "day": d.isoformat()}
    json.dumps(out)


def test_json_safe_coerces_enum_members_to_value():
    class Sev(str, enum.Enum):
        warning = "warning"
        critical = "critical"

    out = _json_safe({"severity": Sev.critical})
    assert out == {"severity": "critical"}
    json.dumps(out)


def test_json_safe_recurses_into_lists():
    u1, u2 = uuid4(), uuid4()
    out = _json_safe({"ids": [u1, u2]})
    assert out == {"ids": [str(u1), str(u2)]}


def test_json_safe_recurses_into_nested_dicts():
    inner = {"site_id": uuid4(), "when": datetime(2026, 1, 1, tzinfo=UTC)}
    out = _json_safe({"diff": inner, "ok": True})
    assert isinstance(out["diff"]["site_id"], str)
    assert isinstance(out["diff"]["when"], str)
    json.dumps(out)


def test_json_safe_handles_sets_and_tuples():
    u = uuid4()
    assert _json_safe((u, u)) == [str(u), str(u)]
    out = _json_safe({u, "k"})
    # set ordering isn't deterministic — assert membership
    assert set(out) == {str(u), "k"}


def test_json_safe_handles_realistic_subnet_patch_diff():
    """The exact shape that triggered the bug in the wild — a subnet
    PATCH with site_id=UUID(...) coming straight out of model_dump."""
    diff = {
        "site_id": UUID("77d7ab68-30c8-44a9-8008-7984f0a4af6d"),
        "vlan_id": 4000,
        "gateway": None,
    }
    out = _json_safe(diff)
    assert out["site_id"] == "77d7ab68-30c8-44a9-8008-7984f0a4af6d"
    assert out["vlan_id"] == 4000
    assert out["gateway"] is None
    json.dumps(out)
