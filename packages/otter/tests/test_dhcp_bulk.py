"""Unit tests for the bulk push/diff summary aggregation (PR 77).

Pure: pins the _tally helper that turns a list of per-scope statuses
into the fixed-key count map the bulk endpoints surface. The async
orchestrators `push_all_scopes` / `diff_all_scopes` are thin loops
over the per-scope versions (already covered in test_dhcp_push.py
and test_dhcp_drift.py) so we exercise the counting logic here, not
the loop itself.
"""

from __future__ import annotations

from dcim.services.dhcp_push import (
    _DIFF_STATUSES,
    _PUSH_STATUSES,
    _tally,
)

# ----- _tally(): fixed-key count map -----

def test_empty_push_status_list_returns_zero_for_each_known_key():
    counts = _tally([], _PUSH_STATUSES)
    assert counts == {"ok": 0, "error": 0, "unsupported": 0}


def test_push_status_counts_aggregate_correctly():
    counts = _tally(
        ["ok", "ok", "error", "ok", "unsupported"], _PUSH_STATUSES,
    )
    assert counts == {"ok": 3, "error": 1, "unsupported": 1}


def test_diff_status_counts_include_all_five_states():
    counts = _tally(
        ["in_sync", "drifted", "missing_from_kea", "never_pushed", "error", "in_sync"],
        _DIFF_STATUSES,
    )
    assert counts == {
        "in_sync": 2,
        "drifted": 1,
        "missing_from_kea": 1,
        "never_pushed": 1,
        "error": 1,
    }


def test_unknown_status_lands_in_other_bucket():
    # Defensive: if a future status slips through without being added
    # to _PUSH_STATUSES, it should surface visibly rather than silently
    # vanish.
    counts = _tally(["ok", "weird-new-state", "ok"], _PUSH_STATUSES)
    assert counts == {"ok": 2, "error": 0, "unsupported": 0, "other": 1}


def test_no_other_key_when_all_statuses_are_known():
    # `other` key only appears when at least one unknown status is
    # observed — keeps the response shape stable for normal traffic.
    counts = _tally(["ok", "ok"], _PUSH_STATUSES)
    assert "other" not in counts


def test_status_taxonomies_exposed_for_endpoint_use():
    # The endpoint code doesn't enumerate these — the API surface
    # depends on these tuples staying source-of-truth.
    assert set(_PUSH_STATUSES) == {"ok", "error", "unsupported"}
    assert set(_DIFF_STATUSES) == {
        "in_sync", "drifted", "missing_from_kea", "never_pushed", "error",
    }
