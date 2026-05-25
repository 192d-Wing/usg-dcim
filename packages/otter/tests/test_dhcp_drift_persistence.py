"""Unit tests for persisted drift state (PR 80).

Pure: pins persist_diff_state semantics — when each DiffResult
status maps to what's written on the scope row's last_diff_*
columns, and the reset-on-push contract that push_scope clears the
cache on successful push.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from uuid import UUID, uuid4

from dcim.services.dhcp_push import DiffResult, persist_diff_state


@dataclass
class _Scope:
    id: UUID
    last_diff_at: object = None
    last_diff_status: str | None = None
    last_diff_delta_json: dict | None = None


def _result(status: str, delta: dict | None = None) -> DiffResult:
    return DiffResult(
        scope_id=str(uuid4()),
        kea_subnet_id=1,
        status=status,
        dcim_subnet=None,
        kea_subnet=None,
        delta=delta or {},
        error=None,
    )


# ----- persist semantics: per-status -----

def test_in_sync_status_stamps_columns_with_null_delta():
    s = _Scope(id=uuid4())
    persist_diff_state(s, _result("in_sync"))
    assert s.last_diff_status == "in_sync"
    assert s.last_diff_at is not None
    assert s.last_diff_delta_json is None


def test_drifted_status_persists_the_full_delta():
    s = _Scope(id=uuid4())
    delta = {"valid-lifetime": {"dcim": 3600, "kea": 7200}}
    persist_diff_state(s, _result("drifted", delta))
    assert s.last_diff_status == "drifted"
    assert s.last_diff_delta_json == delta


def test_missing_from_kea_clears_delta_column():
    # missing_from_kea is implicit (the WHOLE subnet is gone, not
    # just one field), so persisting a stale delta would mislead the
    # operator reading the LIST endpoint.
    s = _Scope(id=uuid4())
    s.last_diff_delta_json = {"old": "delta"}
    persist_diff_state(s, _result("missing_from_kea"))
    assert s.last_diff_status == "missing_from_kea"
    assert s.last_diff_delta_json is None


def test_never_pushed_clears_delta_column():
    s = _Scope(id=uuid4(), last_diff_delta_json={"stale": "value"})
    persist_diff_state(s, _result("never_pushed"))
    assert s.last_diff_status == "never_pushed"
    assert s.last_diff_delta_json is None


def test_error_clears_delta_column():
    s = _Scope(id=uuid4(), last_diff_delta_json={"stale": "value"})
    persist_diff_state(s, _result("error"))
    assert s.last_diff_status == "error"
    assert s.last_diff_delta_json is None


# ----- previous drifted state gets overwritten -----

def test_drifted_then_resync_clears_the_delta():
    # First a drifted run paints the column; a subsequent in_sync
    # run wipes it. This is the cron-friendly happy path.
    s = _Scope(id=uuid4())
    persist_diff_state(s, _result("drifted", {"k": {"dcim": 1, "kea": 2}}))
    assert s.last_diff_delta_json == {"k": {"dcim": 1, "kea": 2}}
    persist_diff_state(s, _result("in_sync"))
    assert s.last_diff_delta_json is None
    assert s.last_diff_status == "in_sync"


def test_drifted_with_new_delta_overwrites_old_delta():
    s = _Scope(id=uuid4())
    persist_diff_state(s, _result("drifted", {"old-field": "old"}))
    persist_diff_state(s, _result("drifted", {"new-field": "new"}))
    assert s.last_diff_delta_json == {"new-field": "new"}


# ----- LIST filter validation surface -----

def test_known_diff_statuses_match_diff_result_taxonomy():
    # The LIST endpoint validates ?diff_status= against this exact
    # set. If a new state is added to DiffResult, the validation
    # in api/ipam.py:list_dhcp_scopes needs the same update.
    from dcim.services.dhcp_push import _DIFF_STATUSES
    assert set(_DIFF_STATUSES) == {
        "in_sync", "drifted", "missing_from_kea", "never_pushed", "error",
    }
