"""PR 97 — wiring tests for the DHCP drift Prometheus gauges.

Pure: pins gauge definitions, label dimensions, the set() contract,
and (via /metrics endpoint) that the series surfaces in the
scrape output. The worker integration runs against a live DB +
arq; that's integration-test territory, not here.
"""

from __future__ import annotations

from dcim import metrics


def test_dhcp_drift_scope_status_gauge_exists_with_three_labels():
    g = metrics.dhcp_drift_scope_status
    assert g._name == "dcim_dhcp_drift_scope_status"
    # prometheus_client exposes the label names on _labelnames.
    assert set(g._labelnames) == {"server_id", "fabric_id", "status"}


def test_dhcp_drift_alerts_firing_gauge_exists_with_two_labels():
    g = metrics.dhcp_drift_alerts_firing
    assert g._name == "dcim_dhcp_drift_alerts_firing"
    assert set(g._labelnames) == {"server_id", "fabric_id"}


def test_drift_scope_status_set_round_trips():
    # Smoke: labeling + .set() doesn't raise, and the value is
    # observable via the child's _value.get() (private API but the
    # canonical way to assert on a Gauge value without a /metrics
    # scrape).
    metrics.dhcp_drift_scope_status.labels(
        server_id="s-test-1", fabric_id="f-test-1", status="drifted",
    ).set(7)
    child = metrics.dhcp_drift_scope_status.labels(
        server_id="s-test-1", fabric_id="f-test-1", status="drifted",
    )
    assert child._value.get() == 7


def test_drift_alerts_firing_set_round_trips():
    metrics.dhcp_drift_alerts_firing.labels(
        server_id="s-test-2", fabric_id="f-test-2",
    ).set(3)
    child = metrics.dhcp_drift_alerts_firing.labels(
        server_id="s-test-2", fabric_id="f-test-2",
    )
    assert child._value.get() == 3


def test_drift_gauges_appear_in_metrics_text():
    # Final integration check: the /metrics scrape includes the new
    # series. Use prometheus_client.generate_latest to dump the
    # current registry state.
    from prometheus_client import generate_latest
    metrics.dhcp_drift_scope_status.labels(
        server_id="s-scrape-1", fabric_id="f-scrape-1", status="in_sync",
    ).set(42)
    metrics.dhcp_drift_alerts_firing.labels(
        server_id="s-scrape-1", fabric_id="f-scrape-1",
    ).set(1)
    output = generate_latest().decode("utf-8")
    assert "dcim_dhcp_drift_scope_status" in output
    assert "dcim_dhcp_drift_alerts_firing" in output
    # The HELP text from the Gauge declaration carries through.
    assert "drift-status bucket" in output
