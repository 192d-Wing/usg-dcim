"""Prometheus metrics — HTTP histograms + business counters.

Exposed at /metrics. Counters are exported as module-level singletons so
the rest of the codebase can do `metrics.alerts_fired.inc()` without
threading a registry through every call site.
"""

from __future__ import annotations

import os
import time

from fastapi import FastAPI, Request
from prometheus_client import (
    CONTENT_TYPE_LATEST,
    CollectorRegistry,
    Counter,
    Gauge,
    Histogram,
    generate_latest,
    multiprocess,
)
from starlette.middleware.base import BaseHTTPMiddleware
from starlette.responses import Response

# DCIM_DISABLE_GO_PORTED_METRICS=true tells otter to STOP emitting the
# Prometheus counters that Go services (magpie, heron) now also emit.
# This is the cutover safety valve: once magpie owns alert evaluation
# and heron owns ingest end-to-end, set this env var so dashboards that
# sum across both services don't double-count.
#
# Affected counters: alerts_fired, alerts_resolved, alert_eval_runs,
# telemetry_samples_ingested, telemetry_ingest_batches,
# telemetry_timescale_writes. The HTTP latency/request counters are
# unaffected — those measure otter's own request handling.
_GO_PORTED_DISABLED = os.getenv("DCIM_DISABLE_GO_PORTED_METRICS", "").lower() in (
    "1", "true", "yes", "on",
)


class _NoopMetric:
    """Drop-in stand-in for prometheus_client Counter/Histogram that
    silently accepts the same call surface but records nothing. Used
    when DCIM_DISABLE_GO_PORTED_METRICS gates a counter the Go services
    now own."""

    def labels(self, **_: object) -> _NoopMetric:
        return self

    def inc(self, *_: object, **__: object) -> None:
        # Intentional no-op: counter is owned by the Go service now.
        return

    def observe(self, *_: object, **__: object) -> None:
        # Intentional no-op: histogram is owned by the Go service now.
        return


def _go_ported(metric: Counter | Histogram) -> Counter | Histogram:
    """Return metric unchanged, or a _NoopMetric when the cutover flag
    is set. Wrapping at definition time avoids touching N call sites."""
    if _GO_PORTED_DISABLED:
        return _NoopMetric()  # type: ignore[return-value]
    return metric

# --- HTTP-level metrics ---
http_requests_total = Counter(
    "dcim_http_requests_total",
    "HTTP requests by method, route, status.",
    ["method", "route", "status"],
)
http_request_duration_seconds = Histogram(
    "dcim_http_request_duration_seconds",
    "End-to-end request latency by method, route.",
    ["method", "route"],
    # Tuned for an internal API: most requests resolve well under 500 ms.
    buckets=(0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0),
)

# --- Business metrics --- these are imported by services + worker ---
# Each is wrapped in _go_ported(): the wrapper returns the real counter
# unless DCIM_DISABLE_GO_PORTED_METRICS is set, in which case it returns
# a no-op so dashboards don't double-count once magpie/heron own these.
telemetry_samples_ingested = _go_ported(Counter(
    "dcim_telemetry_samples_ingested_total",
    "Telemetry samples accepted by the ingest endpoint.",
    ["site_id"],
))
telemetry_ingest_batches = _go_ported(Histogram(
    "dcim_telemetry_ingest_batch_size",
    "Sample count per ingest batch.",
    buckets=(1, 10, 50, 100, 500, 1000, 2500, 5000),
))
# Dual-write to the TimescaleDB hypertable is fail-open during the
# migration. This counter surfaces silent failures so the cutover plan
# doesn't ship blind. outcome = ok | error | disabled.
telemetry_timescale_writes = _go_ported(Counter(
    "dcim_telemetry_timescale_writes_total",
    "Telemetry batches dual-written to the TimescaleDB hypertable.",
    ["outcome"],
))
alerts_fired = _go_ported(Counter(
    "dcim_alerts_fired_total",
    "Alerts transitioned to firing.",
    ["severity"],
))
alerts_resolved = _go_ported(Counter(
    "dcim_alerts_resolved_total",
    "Alerts transitioned to resolved.",
))
alert_eval_runs = _go_ported(Counter(
    "dcim_alert_eval_runs_total",
    "Alert-evaluation worker runs.",
    ["outcome"],  # ok | error
))

# --- DHCP drift gauges (PR 97) ---
# Populated by worker.dhcp_drift_check after each scheduled drift
# pass; values are point-in-time counts that go up and down (a
# drifted scope becomes in_sync after a successful push). Operators
# graph trends in Grafana to spot persistent drift, alert on
# sustained drifted_total > 0, or compare across fabrics.
#
# Labels: server_id and fabric_id let dashboards slice per server
# AND per fabric without a Prometheus join. Status is the same
# taxonomy services.dhcp_push uses (in_sync | drifted |
# missing_from_kea | never_pushed | error). Cardinality is bounded
# by (servers × 5 statuses) + servers — small for any real fleet.
dhcp_drift_scope_status = Gauge(
    "dcim_dhcp_drift_scope_status",
    "Per-server count of DhcpScope rows in each drift-status bucket.",
    ["server_id", "fabric_id", "status"],
)
dhcp_drift_alerts_firing = Gauge(
    "dcim_dhcp_drift_alerts_firing",
    "Per-server count of firing dhcp-drift Alert rows.",
    ["server_id", "fabric_id"],
)

# --- Kea RPC latency (PR 98) ---
# Per-call duration of the Kea Control Agent REST hops we make in
# push_scope (subnetN-add/update), diff_scope (subnetN-get), and
# delete_scope_from_kea (subnetN-del). Operators graph p95/p99 by
# server_id to spot Kea-side slowness before it cascades into
# timeouts; `status` separates the happy path from error paths
# (which often have very different latency distributions).
#
# Buckets are wider than the HTTP-request histogram — Kea reloads
# on subnet_cmds can take a few seconds on busy servers, and we
# want resolution out to ~minute timeouts.
dhcp_kea_call_seconds = Histogram(
    "dcim_dhcp_kea_call_seconds",
    "Kea Control Agent RPC latency by server + operation + status.",
    ["server_id", "operation", "status"],
    buckets=(0.01, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0, 30.0, 60.0),
)

# --- IPAM utilization gauges (PR 99) ---
# Populated by worker.ipam_utilization_sweep on a periodic cadence;
# point-in-time fraction of free address space.
#
# Subnet free%: 100 * (capacity - used) / capacity where capacity is
# the prefix's host count (v4 deducts network + broadcast; v6 deducts
# 2 by convention) and used counts active+reserved IPAddress rows.
# Operators alert when free% < 20 (subnet near exhaustion).
#
# Supernet free%: 100 * (capacity - sum(child subnet capacities))
# / capacity. Tracks carving headroom — how much of the aggregate
# block hasn't been allocated to a subnet yet. Operators alert when
# free% < 10 (supernet near carved-out).
#
# Cardinality: bounded by the row counts of subnets + supernets. A
# fabric with 1000 subnets gets 1000 series, which is fine for any
# realistic Prometheus deployment.
ipam_subnet_free_percent = Gauge(
    "dcim_ipam_subnet_free_percent",
    "Per-subnet percentage of allocatable addresses still free.",
    ["fabric_id", "subnet_id"],
)
ipam_supernet_free_percent = Gauge(
    "dcim_ipam_supernet_free_percent",
    "Per-supernet percentage of address space not yet carved into subnets.",
    ["fabric_id", "supernet_id"],
)


_UNMATCHED_ROUTE = "<unmatched>"


def _route_label(request: Request) -> str:
    """Use the route template (`/inventory/sites/{site_id}`) instead of the
    rendered path so high-cardinality params don't blow up the metric series.

    When `request.scope["route"]` is unset (404s, ASGI sub-apps not yet
    fully routed at middleware time, etc.), the rendered URL path often
    contains UUIDs or numeric IDs that would each create a fresh time
    series. Returning a single placeholder caps cardinality at one extra
    label value across all unmatched paths combined — operators still see
    "look at the 404s" via the `status` label without paying a series
    per offending client."""
    route = request.scope.get("route")
    if route is not None and getattr(route, "path", None):
        return route.path
    return _UNMATCHED_ROUTE


class PrometheusMiddleware(BaseHTTPMiddleware):
    async def dispatch(self, request: Request, call_next) -> Response:  # type: ignore[override]
        started = time.perf_counter()
        status_code = 500
        try:
            response = await call_next(request)
            status_code = response.status_code
            return response
        finally:
            route = _route_label(request)
            # Don't pollute the histograms with /metrics scrapes from Prometheus.
            if route != "/metrics":
                elapsed = time.perf_counter() - started
                http_requests_total.labels(
                    method=request.method, route=route, status=str(status_code),
                ).inc()
                http_request_duration_seconds.labels(
                    method=request.method, route=route,
                ).observe(elapsed)


def install(app: FastAPI) -> None:
    app.add_middleware(PrometheusMiddleware)

    @app.get("/metrics", include_in_schema=False)
    async def metrics() -> Response:
        # Honor multi-process mode if/when the API runs under gunicorn workers.
        registry: CollectorRegistry
        try:
            registry = CollectorRegistry()
            multiprocess.MultiProcessCollector(registry)
        except (ValueError, KeyError):
            from prometheus_client import REGISTRY
            registry = REGISTRY  # type: ignore[assignment]
        return Response(generate_latest(registry), media_type=CONTENT_TYPE_LATEST)
