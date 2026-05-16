"""Unit tests for the alert engine's pure pieces.

The full evaluator hits Elastic + Postgres, so this module covers the parts
that are pure: dedupe key construction and the threshold operator table.
"""

import operator

from dcim.services.alerts import _OPS, asset_matches_filter, dedupe_key


def test_dedupe_key_is_stable_for_same_inputs():
    a = dedupe_key("rule-1", "asset-x", "pdu.input.kw")
    b = dedupe_key("rule-1", "asset-x", "pdu.input.kw")
    assert a == b


def test_dedupe_key_changes_with_any_input():
    base = dedupe_key("rule-1", "asset-x", "pdu.input.kw")
    assert dedupe_key("rule-2", "asset-x", "pdu.input.kw") != base
    assert dedupe_key("rule-1", "asset-y", "pdu.input.kw") != base
    assert dedupe_key("rule-1", "asset-x", "sensor.temp.c") != base


def test_dedupe_key_format_is_pipe_delimited_for_simple_parsing():
    """Downstream tooling splits on '|' to extract rule_id; pin the format."""
    k = dedupe_key("r", "a", "m")
    assert k == "r|a|m"
    assert len(k.split("|")) == 3


def test_ops_table_covers_all_supported_operators():
    assert set(_OPS.keys()) == {">", ">=", "<", "<=", "==", "!="}


def test_ops_table_maps_to_correct_python_operators():
    cases = [
        (">", operator.gt, 5, 4, True),
        (">", operator.gt, 4, 5, False),
        (">=", operator.ge, 5, 5, True),
        ("<", operator.lt, 4, 5, True),
        ("<=", operator.le, 5, 5, True),
        ("==", operator.eq, 5, 5, True),
        ("!=", operator.ne, 5, 4, True),
    ]
    for op_str, expected_fn, lhs, rhs, expected_result in cases:
        assert _OPS[op_str] is expected_fn
        assert _OPS[op_str](lhs, rhs) is expected_result


# --- asset_matches_filter (maintenance window predicate) -----------------

class _FakeEnum:
    """Stand-in for SQLAlchemy enum columns: has `.value` like real enums."""
    def __init__(self, value: str):
        self.value = value


_PDU_ASSET = {
    "kind": _FakeEnum("pdu"),
    "manufacturer": "Vertiv",
    "model": "PD123",
    "rack_id": "rack-1",
    "lifecycle_state": _FakeEnum("active"),
}


def test_empty_filter_matches_everything():
    assert asset_matches_filter(_PDU_ASSET, {}) is True


def test_scalar_equality_match():
    assert asset_matches_filter(_PDU_ASSET, {"kind": "pdu"}) is True
    assert asset_matches_filter(_PDU_ASSET, {"kind": "server"}) is False


def test_list_membership_match():
    assert asset_matches_filter(_PDU_ASSET, {"kind": ["pdu", "ups"]}) is True
    assert asset_matches_filter(_PDU_ASSET, {"kind": ["server", "switch"]}) is False


def test_multi_key_filter_is_and_semantics():
    f = {"kind": "pdu", "manufacturer": "Vertiv"}
    assert asset_matches_filter(_PDU_ASSET, f) is True
    f["manufacturer"] = "Eaton"
    assert asset_matches_filter(_PDU_ASSET, f) is False


def test_unknown_key_is_fail_safe_miss():
    # Better to fire a spurious alert than silently suppress on a typo.
    assert asset_matches_filter(_PDU_ASSET, {"site_id": "anything"}) is False
    assert asset_matches_filter(_PDU_ASSET, {"kind": "pdu", "bogus": "x"}) is False


def test_missing_asset_attr_is_a_miss():
    # If the asset doesn't carry the attr the operator filtered on, don't suppress.
    asset = {"kind": _FakeEnum("server"), "manufacturer": None}
    assert asset_matches_filter(asset, {"manufacturer": "Dell"}) is False
