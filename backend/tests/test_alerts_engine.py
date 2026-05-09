"""Unit tests for the alert engine's pure pieces.

The full evaluator hits Elastic + Postgres, so this module covers the parts
that are pure: dedupe key construction and the threshold operator table.
"""

import operator

from dcim.services.alerts import _OPS, dedupe_key


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
