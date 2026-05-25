"""IPAM utilization computation (PR 99).

Pure helpers + a worker-side orchestrator that pushes utilization
gauges to Prometheus. The cron task in worker.py loads Subnets,
Supernets, and per-subnet IPAddress counts in three SELECTs and
folds them into per-row free percentages via the helpers here.

Why "free percent" not "used percent": operators alert on
exhaustion (free < threshold) more often than on consumption
(used > threshold). Picking the same orientation across both
gauges keeps Grafana queries symmetric.
"""

from __future__ import annotations

import ipaddress
from dataclasses import dataclass


@dataclass
class SubnetUtilization:
    subnet_id: str
    fabric_id: str
    prefix: str
    capacity: int
    used: int
    free_percent: float


@dataclass
class SupernetUtilization:
    supernet_id: str
    fabric_id: str
    prefix: str
    capacity: int
    carved: int
    free_percent: float


def _prefix_capacity(prefix: str) -> int:
    """Allocatable host count for a CIDR prefix.

    v4: num_addresses - 2 (network + broadcast). For /31 and /32
    point-to-point and host routes we keep the formula but the value
    can go to 0 or 1; the gauge clamps cleanly because the caller
    division check handles capacity == 0.

    v6: num_addresses - 2 by convention (network address +
    anycast/subnet-router-anycast); /128 gets capacity = -1 which
    we clamp to 0.
    """
    try:
        net = ipaddress.ip_network(prefix.strip(), strict=False)
    except (TypeError, ValueError):
        return 0
    cap = max(0, int(net.num_addresses) - 2)
    return cap


def compute_subnet_utilization(
    subnet_id: str,
    fabric_id: str,
    prefix: str,
    used_count: int,
) -> SubnetUtilization:
    """Build a SubnetUtilization from the row + a pre-fetched count.

    Caller runs `SELECT subnet_id, COUNT(*) FROM ip_addresses WHERE
    status IN ('active','reserved') GROUP BY subnet_id` once and
    feeds the counts in. capacity == 0 → free_percent = 100 (a /32
    point-to-point with no consumers is "100% free" — there's
    nothing TO use).
    """
    cap = _prefix_capacity(prefix)
    if cap == 0:
        return SubnetUtilization(
            subnet_id=subnet_id, fabric_id=fabric_id, prefix=prefix,
            capacity=0, used=int(used_count or 0), free_percent=100.0,
        )
    used = min(int(used_count or 0), cap)  # cap usage at capacity
    free_pct = 100.0 * (cap - used) / cap
    return SubnetUtilization(
        subnet_id=subnet_id, fabric_id=fabric_id, prefix=prefix,
        capacity=cap, used=used, free_percent=free_pct,
    )


def compute_supernet_utilization(
    supernet_id: str,
    fabric_id: str,
    prefix: str,
    carved_capacity: int,
) -> SupernetUtilization:
    """Build a SupernetUtilization from the row + sum of child
    subnet capacities.

    carved_capacity is the sum of host counts across every Subnet
    whose supernet_id points at this row. Caller computes it from
    the same per-subnet capacities the SubnetUtilization helper
    produces, then groups by supernet.

    A supernet's capacity uses the raw num_addresses (no -2 deduct)
    since the calculation is "how much of the address SPACE is
    carved into subnets," not "how many client addresses are
    allocatable."
    """
    try:
        net = ipaddress.ip_network(prefix.strip(), strict=False)
        cap = int(net.num_addresses)
    except (TypeError, ValueError):
        cap = 0
    if cap == 0:
        return SupernetUtilization(
            supernet_id=supernet_id, fabric_id=fabric_id, prefix=prefix,
            capacity=0, carved=int(carved_capacity or 0), free_percent=100.0,
        )
    carved = min(int(carved_capacity or 0), cap)
    free_pct = 100.0 * (cap - carved) / cap
    return SupernetUtilization(
        supernet_id=supernet_id, fabric_id=fabric_id, prefix=prefix,
        capacity=cap, carved=carved, free_percent=free_pct,
    )
