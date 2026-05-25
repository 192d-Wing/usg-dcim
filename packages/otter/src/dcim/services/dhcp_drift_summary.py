"""Fleet-wide DHCP drift aggregation (PR 93).

GET /api/v1/ipam/dhcp/drift-summary returns per-server scope-status
counts plus a fleet roll-up. Operators previously had to walk every
DhcpServer + call LIST ?diff_status=drifted per server to assemble
the same view; this endpoint does it in two SELECTs.

Pure-ish: takes loaded DhcpServer rows + DhcpScope rows + firing
Alert rows and produces the shape the API handler returns. The
handler owns the SELECTs (with ABAC fabric-scope filter applied)
so the service stays decoupled from the DB session.

Alert count is keyed on dedupe_key LIKE 'dhcp-drift:%' (the prefix
PR 87 uses) rather than labels_json contents — JSONB filters need
extra indexes; the prefix is a substring of an indexed column.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Iterable


# Status taxonomy mirrors services.dhcp_push._DIFF_STATUSES.
_DIFF_STATUSES = ("in_sync", "drifted", "missing_from_kea", "never_pushed", "error")


@dataclass
class ServerDriftSummary:
    server_id: str
    server_name: str
    fabric_id: str
    enabled: bool
    last_push_at: object  # datetime | None
    last_push_status: str | None
    scope_counts: dict[str, int] = field(default_factory=dict)
    scopes_total: int = 0
    alerts_firing: int = 0


@dataclass
class FleetDriftSummary:
    servers_total: int
    servers_with_drift: int
    scopes_total: int
    scope_counts: dict[str, int]
    alerts_firing: int


def _scope_count_template() -> dict[str, int]:
    """Fixed-key map so the API response shape is stable per status,
    even when no scope on a server has reached a given state yet."""
    return dict.fromkeys(_DIFF_STATUSES, 0)


def aggregate(
    servers: Iterable,
    scopes_by_server: dict,
    alert_counts_by_dedupe_prefix: dict,
) -> tuple[FleetDriftSummary, list[ServerDriftSummary]]:
    """Build per-server summaries + fleet roll-up.

    `scopes_by_server` maps server.id (UUID) → list[DhcpScope].
    `alert_counts_by_dedupe_prefix` maps each scope.id (str) → firing
    Alert count (0 or 1 since the dedupe_key is per-scope). The
    handler builds both via the bulk Alert SELECT.

    Servers with `last_diff_status=None` (e.g. never pushed or just
    created) contribute to scopes_total but not to any specific
    bucket; we count them in `never_pushed` since that's what they
    are from the operator's POV.
    """
    fleet_counts = _scope_count_template()
    fleet_alerts = 0
    servers_with_drift = 0
    summaries: list[ServerDriftSummary] = []
    servers_total = 0
    scopes_total = 0

    for srv in servers:
        servers_total += 1
        scope_rows = scopes_by_server.get(srv.id, [])
        counts = _scope_count_template()
        server_alerts = 0
        for sc in scope_rows:
            status = sc.last_diff_status or "never_pushed"
            if status not in counts:
                # Defensive: shouldn't happen with current taxonomy.
                # Bucket into 'error' so the row surfaces visibly.
                status = "error"
            counts[status] += 1
            server_alerts += alert_counts_by_dedupe_prefix.get(str(sc.id), 0)
        scopes_total_for_server = len(scope_rows)
        scopes_total += scopes_total_for_server
        for k, v in counts.items():
            fleet_counts[k] += v
        fleet_alerts += server_alerts
        if counts.get("drifted", 0) > 0:
            servers_with_drift += 1
        summaries.append(ServerDriftSummary(
            server_id=str(srv.id),
            server_name=srv.name,
            fabric_id=str(srv.fabric_id),
            enabled=bool(srv.enabled),
            last_push_at=srv.last_push_at,
            last_push_status=srv.last_push_status,
            scope_counts=counts,
            scopes_total=scopes_total_for_server,
            alerts_firing=server_alerts,
        ))

    fleet = FleetDriftSummary(
        servers_total=servers_total,
        servers_with_drift=servers_with_drift,
        scopes_total=scopes_total,
        scope_counts=fleet_counts,
        alerts_firing=fleet_alerts,
    )
    return fleet, summaries
