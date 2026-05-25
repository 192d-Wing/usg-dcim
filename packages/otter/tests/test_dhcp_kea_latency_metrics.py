"""PR 98 — wiring tests for the Kea RPC latency histogram.

Pure: pins the histogram definition + the _kea_call_timer context
manager contract. The actual push/diff/delete call paths run
against a live Kea via integration tests; here we exercise the
timer in isolation.
"""

from __future__ import annotations

from prometheus_client import generate_latest

from dcim import metrics
from dcim.services.dhcp_push import _kea_call_timer


def test_kea_call_seconds_histogram_exists_with_three_labels():
    h = metrics.dhcp_kea_call_seconds
    assert h._name == "dcim_dhcp_kea_call_seconds"
    assert set(h._labelnames) == {"server_id", "operation", "status"}


def test_kea_call_seconds_has_wide_buckets_for_kea_reload_times():
    h = metrics.dhcp_kea_call_seconds
    # Buckets must extend out past 10s — a Kea subnet_cmds reload on
    # a busy server can take >5s. The HTTP-request histogram tops
    # out at 10s; this one needs more headroom.
    upper = h._upper_bounds
    assert max(upper) >= 60.0  # 60s ceiling for "timeout-ish"


def test_timer_records_status_ok_when_stamped():
    # Round-trip: enter the timer, stamp status="ok", verify the
    # bucket increments. Reads via _sum/_count internals; the
    # generate_latest path is exercised by the next test.
    with _kea_call_timer("s-timer-1", "push") as t:
        t.status = "ok"
        # No actual RPC; the time delta is microseconds.
    # The "ok" series should exist with at least one observation.
    sample_count = 0
    for metric in metrics.dhcp_kea_call_seconds.collect():
        for s in metric.samples:
            if (
                s.name.endswith("_count")
                and s.labels.get("server_id") == "s-timer-1"
                and s.labels.get("status") == "ok"
                and s.labels.get("operation") == "push"
            ):
                sample_count = s.value
    assert sample_count >= 1


def test_timer_defaults_status_to_error_on_exception():
    # If the caller raises inside the with, the timer's
    # default "error" status survives — the metric still records
    # the duration of the failed RPC.
    try:
        with _kea_call_timer("s-timer-2", "diff"):
            raise RuntimeError("simulated kea transport blow-up")
    except RuntimeError:
        pass
    # An "error"-labelled sample should now exist for this server.
    found = False
    for metric in metrics.dhcp_kea_call_seconds.collect():
        for s in metric.samples:
            if (
                s.name.endswith("_count")
                and s.labels.get("server_id") == "s-timer-2"
                and s.labels.get("status") == "error"
            ):
                found = s.value >= 1
    assert found, "error-labelled diff observation not recorded"


def test_kea_latency_appears_in_scrape_output():
    with _kea_call_timer("s-scrape-1", "delete") as t:
        t.status = "ok"
    output = generate_latest().decode("utf-8")
    assert "dcim_dhcp_kea_call_seconds" in output
    assert 's-scrape-1' in output
    # Histogram emits _bucket / _count / _sum lines.
    assert "dcim_dhcp_kea_call_seconds_bucket" in output
    assert "dcim_dhcp_kea_call_seconds_count" in output
    assert "dcim_dhcp_kea_call_seconds_sum" in output
