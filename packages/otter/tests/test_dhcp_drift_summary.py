"""PR 93 — unit tests for the fleet drift aggregator.

Pure: pins the per-server count rollup, the fleet roll-up, the
servers_with_drift threshold, the never_pushed fallback when
last_diff_status is NULL, and the alert-count attribution. The
SQL paths run against the DB in integration tests; here the
aggregator is given pre-loaded fixtures.
"""

from __future__ import annotations

from dataclasses import dataclass
from uuid import UUID, uuid4

from dcim.services.dhcp_drift_summary import aggregate


@dataclass
class _Server:
    id: UUID
    name: str
    fabric_id: UUID
    enabled: bool = True
    last_push_at: object = None
    last_push_status: str | None = None


@dataclass
class _Scope:
    id: UUID
    last_diff_status: str | None


def _server(name: str = "kea-east-1") -> _Server:
    return _Server(id=uuid4(), name=name, fabric_id=uuid4())


# ----- empty fleet -----

def test_empty_server_list_produces_zero_fleet_summary():
    fleet, _fabrics, summaries = aggregate([], {}, {})
    assert fleet.servers_total == 0
    assert fleet.servers_with_drift == 0
    assert fleet.scopes_total == 0
    assert fleet.alerts_firing == 0
    assert summaries == []
    # Fixed-key shape: every taxonomy state appears with zero count
    # so the API response is stable for empty fleets too.
    assert set(fleet.scope_counts) == {
        "in_sync", "drifted", "missing_from_kea", "never_pushed", "error",
    }


# ----- per-server counts -----

def test_single_server_with_mixed_scopes_aggregates_counts():
    srv = _server()
    scopes = [
        _Scope(id=uuid4(), last_diff_status="in_sync"),
        _Scope(id=uuid4(), last_diff_status="in_sync"),
        _Scope(id=uuid4(), last_diff_status="drifted"),
        _Scope(id=uuid4(), last_diff_status="missing_from_kea"),
    ]
    fleet, _fabrics, summaries = aggregate(
        [srv], {srv.id: scopes}, {},
    )
    assert len(summaries) == 1
    s = summaries[0]
    assert s.scopes_total == 4
    assert s.scope_counts == {
        "in_sync": 2, "drifted": 1, "missing_from_kea": 1,
        "never_pushed": 0, "error": 0,
    }
    assert fleet.scopes_total == 4
    assert fleet.servers_with_drift == 1


# ----- NULL status fallback -----

def test_null_last_diff_status_counts_as_never_pushed():
    # Fresh scope just created; cron hasn't run yet. The aggregator
    # buckets NULL into never_pushed since that's the operator's
    # view (it's been authored but never checked against Kea).
    srv = _server()
    scopes = [
        _Scope(id=uuid4(), last_diff_status=None),
        _Scope(id=uuid4(), last_diff_status=None),
        _Scope(id=uuid4(), last_diff_status="in_sync"),
    ]
    _fleet, _fabrics, summaries = aggregate([srv], {srv.id: scopes}, {})
    s = summaries[0]
    assert s.scope_counts["never_pushed"] == 2
    assert s.scope_counts["in_sync"] == 1
    assert s.scopes_total == 3


# ----- servers_with_drift threshold -----

def test_servers_with_drift_counts_servers_with_any_drifted_scope():
    a, b, c = _server("a"), _server("b"), _server("c")
    fleet, _fabrics, _summaries = aggregate(
        [a, b, c],
        {
            a.id: [_Scope(id=uuid4(), last_diff_status="in_sync")],
            b.id: [_Scope(id=uuid4(), last_diff_status="drifted")],
            c.id: [
                _Scope(id=uuid4(), last_diff_status="drifted"),
                _Scope(id=uuid4(), last_diff_status="in_sync"),
            ],
        },
        {},
    )
    # b and c have drift; a doesn't.
    assert fleet.servers_with_drift == 2
    assert fleet.servers_total == 3


def test_clean_fleet_reports_zero_servers_with_drift():
    srv = _server()
    fleet, _fabrics, _summaries = aggregate(
        [srv], {srv.id: [_Scope(id=uuid4(), last_diff_status="in_sync")]}, {},
    )
    assert fleet.servers_with_drift == 0
    assert fleet.scope_counts["in_sync"] == 1
    assert fleet.scope_counts["drifted"] == 0


# ----- alert count attribution -----

def test_alert_counts_attribute_to_the_owning_server():
    a, b = _server("a"), _server("b")
    sa, sb1, sb2 = uuid4(), uuid4(), uuid4()
    fleet, _fabrics, summaries = aggregate(
        [a, b],
        {
            a.id: [_Scope(id=sa, last_diff_status="drifted")],
            b.id: [
                _Scope(id=sb1, last_diff_status="drifted"),
                _Scope(id=sb2, last_diff_status="drifted"),
            ],
        },
        {str(sa): 1, str(sb1): 1, str(sb2): 1},
    )
    by_name = {s.server_name: s for s in summaries}
    assert by_name["a"].alerts_firing == 1
    assert by_name["b"].alerts_firing == 2
    assert fleet.alerts_firing == 3


def test_no_alerts_yields_zero_counts():
    srv = _server()
    fleet, _fabrics, summaries = aggregate(
        [srv], {srv.id: [_Scope(id=uuid4(), last_diff_status="drifted")]}, {},
    )
    assert fleet.alerts_firing == 0
    assert summaries[0].alerts_firing == 0


# ----- unknown status defensiveness -----

def test_unknown_status_falls_into_error_bucket():
    # If a future status slips into the column without being added to
    # the taxonomy, the aggregator should surface it visibly rather
    # than silently dropping it.
    srv = _server()
    fleet, _fabrics, summaries = aggregate(
        [srv], {srv.id: [_Scope(id=uuid4(), last_diff_status="weird-new")]}, {},
    )
    assert summaries[0].scope_counts["error"] == 1
    assert fleet.scope_counts["error"] == 1


# ----- server identity passthrough -----

def test_server_summary_carries_through_metadata():
    srv = _Server(
        id=uuid4(), name="kea-foo", fabric_id=uuid4(),
        enabled=False, last_push_at=None, last_push_status="error",
    )
    _fleet, _fabrics, summaries = aggregate([srv], {srv.id: []}, {})
    s = summaries[0]
    assert s.server_id == str(srv.id)
    assert s.server_name == "kea-foo"
    assert s.fabric_id == str(srv.fabric_id)
    assert s.enabled is False
    assert s.last_push_status == "error"


# ----- PR 102: per-fabric roll-up -----

def test_empty_fleet_produces_empty_fabric_list():
    _fleet, fabrics, _summaries = aggregate([], {}, {})
    assert fabrics == []


def test_single_fabric_single_server_emits_one_fabric_summary():
    srv = _server()
    fid_str = str(srv.fabric_id)
    scopes = [
        _Scope(id=uuid4(), last_diff_status="in_sync"),
        _Scope(id=uuid4(), last_diff_status="drifted"),
    ]
    _fleet, fabrics, _summaries = aggregate(
        [srv], {srv.id: scopes}, {},
    )
    assert len(fabrics) == 1
    fab = fabrics[0]
    assert fab.fabric_id == fid_str
    assert fab.servers_total == 1
    assert fab.servers_with_drift == 1
    assert fab.scopes_total == 2
    assert fab.scope_counts["drifted"] == 1
    assert fab.scope_counts["in_sync"] == 1


def test_multiple_fabrics_each_get_their_own_slice():
    f1, f2 = uuid4(), uuid4()
    a = _Server(id=uuid4(), name="srv-a", fabric_id=f1)
    b = _Server(id=uuid4(), name="srv-b", fabric_id=f1)
    c = _Server(id=uuid4(), name="srv-c", fabric_id=f2)
    _fleet, fabrics, _summaries = aggregate(
        [a, b, c],
        {
            a.id: [_Scope(id=uuid4(), last_diff_status="drifted")],
            b.id: [_Scope(id=uuid4(), last_diff_status="in_sync")],
            c.id: [_Scope(id=uuid4(), last_diff_status="drifted")],
        },
        {},
    )
    by_id = {f.fabric_id: f for f in fabrics}
    # f1 has 2 servers, 1 drifted (a only); 2 scopes total.
    assert by_id[str(f1)].servers_total == 2
    assert by_id[str(f1)].servers_with_drift == 1
    assert by_id[str(f1)].scopes_total == 2
    assert by_id[str(f1)].scope_counts["drifted"] == 1
    # f2 has 1 server, fully drifted.
    assert by_id[str(f2)].servers_total == 1
    assert by_id[str(f2)].servers_with_drift == 1


def test_fabric_alert_counts_aggregate_per_fabric():
    f1, f2 = uuid4(), uuid4()
    a = _Server(id=uuid4(), name="srv-a", fabric_id=f1)
    b = _Server(id=uuid4(), name="srv-b", fabric_id=f2)
    sa, sb = uuid4(), uuid4()
    _fleet, fabrics, _summaries = aggregate(
        [a, b],
        {
            a.id: [_Scope(id=sa, last_diff_status="drifted")],
            b.id: [_Scope(id=sb, last_diff_status="drifted")],
        },
        {str(sa): 1, str(sb): 1},
    )
    by_id = {f.fabric_id: f for f in fabrics}
    assert by_id[str(f1)].alerts_firing == 1
    assert by_id[str(f2)].alerts_firing == 1


def test_fabric_slice_sum_matches_fleet():
    # Invariant: summing per-fabric scopes_total + alerts equals
    # the fleet roll-up. Useful as a sanity check; if a future
    # refactor double-counts or drops a server, this catches it.
    f1, f2 = uuid4(), uuid4()
    a = _Server(id=uuid4(), name="srv-a", fabric_id=f1)
    b = _Server(id=uuid4(), name="srv-b", fabric_id=f2)
    fleet, fabrics, _summaries = aggregate(
        [a, b],
        {
            a.id: [
                _Scope(id=uuid4(), last_diff_status="drifted"),
                _Scope(id=uuid4(), last_diff_status="in_sync"),
            ],
            b.id: [_Scope(id=uuid4(), last_diff_status="error")],
        },
        {},
    )
    assert sum(f.scopes_total for f in fabrics) == fleet.scopes_total
    assert sum(f.servers_total for f in fabrics) == fleet.servers_total
    assert sum(f.alerts_firing for f in fabrics) == fleet.alerts_firing
