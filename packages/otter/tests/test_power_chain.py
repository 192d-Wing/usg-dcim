"""Unit tests for the redundancy classifier.

`compute_power_chain` is async + DB-bound; the meaningful logic
(redundancy verdict from a list of connections) is extracted into the
pure `classify_redundancy` helper, exercised here.
"""

from dcim.models.inventory import AssetKind
from dcim.services.power_chain import classify_redundancy


def _conn(side: str | None, *, psu: int = 1) -> dict:
    return {"pdu_side": side, "psu_index": psu}


def test_pdu_kind_returns_na_regardless_of_connections():
    sides, verdict = classify_redundancy(AssetKind.pdu, [])
    assert verdict == "n/a"
    assert sides == []
    sides, verdict = classify_redundancy(AssetKind.pdu, [_conn("A"), _conn("B")])
    assert verdict == "n/a"


def test_pdu_value_string_also_maps_to_na():
    """Callers may pass the enum or the string value; both must work."""
    sides, verdict = classify_redundancy("pdu", [_conn("A")])
    assert verdict == "n/a"
    assert sides == []


def test_no_connections_is_unpowered():
    sides, verdict = classify_redundancy(AssetKind.server, [])
    assert verdict == "unpowered"
    assert sides == []


def test_all_connections_on_one_side_is_single():
    sides, verdict = classify_redundancy(
        AssetKind.server, [_conn("A", psu=1), _conn("A", psu=2)],
    )
    assert verdict == "single"
    assert sides == ["A"]


def test_two_distinct_sides_is_redundant():
    sides, verdict = classify_redundancy(
        AssetKind.server, [_conn("A", psu=1), _conn("B", psu=2)],
    )
    assert verdict == "redundant"
    assert sides == ["A", "B"]


def test_three_distinct_sides_still_redundant():
    sides, verdict = classify_redundancy(
        AssetKind.server,
        [_conn("A"), _conn("B"), _conn("C")],
    )
    assert verdict == "redundant"
    assert sides == ["A", "B", "C"]


def test_connection_with_unknown_side_does_not_count_for_redundancy():
    """A connection without a pdu_side label still counts as 'has power' (so
    the verdict isn't 'unpowered'), but it can't satisfy the 2-side rule."""
    sides, verdict = classify_redundancy(
        AssetKind.server, [_conn(None), _conn("A")],
    )
    assert verdict == "single"
    assert sides == ["A"]


def test_only_unknown_sides_is_still_powered_just_single():
    sides, verdict = classify_redundancy(
        AssetKind.server, [_conn(None), _conn(None)],
    )
    assert verdict == "single"
    assert sides == []
