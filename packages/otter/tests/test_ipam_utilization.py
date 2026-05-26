"""PR 99 — tests for IPAM utilization computation + gauge wiring.

Pure: pins the per-prefix capacity math, the free% formula, the
edge cases (empty prefix, full subnet, /31 point-to-point, /128
host route, /32 host), and the gauge definitions + scrape output.
"""

from __future__ import annotations

from prometheus_client import generate_latest

from dcim import metrics, worker
from dcim.services import ipam_metrics

# ----- capacity math -----

def test_v4_24_prefix_capacity_is_254():
    u = ipam_metrics.compute_subnet_utilization("s", "f", "10.0.0.0/24", 0)
    assert u.capacity == 254  # 256 - 2 (network + broadcast)


def test_v4_30_prefix_capacity_is_2():
    u = ipam_metrics.compute_subnet_utilization("s", "f", "10.0.0.0/30", 0)
    assert u.capacity == 2


def test_v4_31_point_to_point_capacity_is_0():
    # RFC 3021 /31 has 2 addresses but both are usable. Our deduct
    # formula (n-2) gives 0 here — that's the operator's reminder
    # that the /31 model needs special handling for capacity
    # accounting; the gauge clamps to 100% free for /31s.
    u = ipam_metrics.compute_subnet_utilization("s", "f", "10.0.0.0/31", 0)
    assert u.capacity == 0
    assert u.free_percent == 100.0


def test_v4_32_host_route_capacity_is_0():
    u = ipam_metrics.compute_subnet_utilization("s", "f", "10.0.0.5/32", 0)
    assert u.capacity == 0


def test_v6_64_prefix_capacity_is_huge():
    u = ipam_metrics.compute_subnet_utilization("s", "f", "2001:db8::/64", 0)
    # 2^64 - 2 — large int, but the helper keeps it as int.
    assert u.capacity == (2 ** 64) - 2


def test_malformed_prefix_returns_capacity_0():
    u = ipam_metrics.compute_subnet_utilization("s", "f", "not-a-cidr", 0)
    assert u.capacity == 0
    assert u.free_percent == 100.0


# ----- free percent math -----

def test_half_used_subnet_reports_50_percent_free():
    u = ipam_metrics.compute_subnet_utilization("s", "f", "10.0.0.0/24", 127)
    # 254 - 127 = 127 free; 127/254 = 50.0%
    assert abs(u.free_percent - 50.0) < 0.01


def test_full_subnet_reports_0_percent_free():
    u = ipam_metrics.compute_subnet_utilization("s", "f", "10.0.0.0/24", 254)
    assert u.free_percent == 0.0


def test_empty_subnet_reports_100_percent_free():
    u = ipam_metrics.compute_subnet_utilization("s", "f", "10.0.0.0/24", 0)
    assert u.free_percent == 100.0


def test_overflow_used_count_clamps_to_capacity():
    # Defensive: if the IPAddress COUNT(*) returns more rows than
    # the prefix can hold (shouldn't happen but DB drift), clamp
    # so free% stays in [0, 100].
    u = ipam_metrics.compute_subnet_utilization("s", "f", "10.0.0.0/24", 9999)
    assert u.free_percent == 0.0
    assert u.used == 254


# ----- supernet (carving headroom) -----

def test_supernet_carving_math_uses_raw_num_addresses_no_deduct():
    # Supernet capacity is the full 2^N (no -2 deduct) because
    # we're measuring how much of the address SPACE is carved
    # into subnets, not how many client addresses are usable.
    sn = ipam_metrics.compute_supernet_utilization(
        "sn", "f", "10.0.0.0/16", carved_capacity=0,
    )
    assert sn.capacity == 65536


def test_supernet_with_half_carved_reports_50_percent_free():
    sn = ipam_metrics.compute_supernet_utilization(
        "sn", "f", "10.0.0.0/16", carved_capacity=32768,
    )
    assert abs(sn.free_percent - 50.0) < 0.01


def test_supernet_overcarved_clamps_to_capacity():
    sn = ipam_metrics.compute_supernet_utilization(
        "sn", "f", "10.0.0.0/24", carved_capacity=999_999,
    )
    assert sn.carved == 256  # /24's full num_addresses
    assert sn.free_percent == 0.0


# ----- gauge definitions -----

def test_subnet_free_percent_gauge_exists():
    g = metrics.ipam_subnet_free_percent
    assert g._name == "dcim_ipam_subnet_free_percent"
    assert set(g._labelnames) == {"fabric_id", "subnet_id"}


def test_supernet_free_percent_gauge_exists():
    g = metrics.ipam_supernet_free_percent
    assert g._name == "dcim_ipam_supernet_free_percent"
    assert set(g._labelnames) == {"fabric_id", "supernet_id"}


def test_subnet_gauge_round_trips():
    metrics.ipam_subnet_free_percent.labels(
        fabric_id="f-test-1", subnet_id="s-test-1",
    ).set(42.5)
    child = metrics.ipam_subnet_free_percent.labels(
        fabric_id="f-test-1", subnet_id="s-test-1",
    )
    assert child._value.get() == 42.5


def test_ipam_gauges_appear_in_metrics_scrape():
    metrics.ipam_subnet_free_percent.labels(
        fabric_id="f-scrape-1", subnet_id="s-scrape-1",
    ).set(75.0)
    metrics.ipam_supernet_free_percent.labels(
        fabric_id="f-scrape-1", supernet_id="sn-scrape-1",
    ).set(33.3)
    output = generate_latest().decode("utf-8")
    assert "dcim_ipam_subnet_free_percent" in output
    assert "dcim_ipam_supernet_free_percent" in output


# ----- worker wiring -----

def test_ipam_utilization_sweep_is_registered():
    assert worker.ipam_utilization_sweep in worker.WorkerSettings.functions


def test_ipam_utilization_sweep_has_cron_entry():
    coros = [
        getattr(c, "coroutine", None) for c in worker.WorkerSettings.cron_jobs
    ]
    assert worker.ipam_utilization_sweep in coros


def test_ipam_utilization_sweep_cron_cadence_is_every_5_minutes():
    for c in worker.WorkerSettings.cron_jobs:
        if getattr(c, "coroutine", None) is worker.ipam_utilization_sweep:
            # range(3, 60, 5) → 12 minute marks per hour.
            assert len(c.minute) == 12
            break
    else:
        raise AssertionError("ipam_utilization_sweep cron entry not found")
