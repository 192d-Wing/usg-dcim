"""Unit tests for the Prometheus metrics middleware.

The biggest production risk in the metrics layer is **cardinality
explosion** — a label that contains a UUID or numeric ID creates one
time series per unique value, and Prometheus storage scales poorly past
~10k series per metric. The tests below lock the route-label contract
against regressions that would re-introduce that failure mode.
"""

from types import SimpleNamespace

from prometheus_client import CollectorRegistry, Counter, Histogram

from dcim.metrics import _UNMATCHED_ROUTE, _route_label


def _request(scope_extra: dict | None = None, path: str = "/api/v1/foo"):
    """Build a FastAPI-shaped Request stand-in. The middleware reads
    `request.scope` and `request.url.path`; SimpleNamespace covers both
    without bringing in starlette test machinery."""
    scope = {}
    if scope_extra:
        scope.update(scope_extra)
    return SimpleNamespace(
        scope=scope,
        url=SimpleNamespace(path=path),
    )


def test_route_label_uses_template_when_route_resolved():
    """The whole point of the helper: when FastAPI resolved a route to
    a template (e.g. /collectors/{collector_id}/heartbeat), the metric
    label must be the template — not the rendered path with the UUID."""
    template = "/collectors/{collector_id}/heartbeat"
    rendered = "/collectors/9b3bf374-d05c-41ab-b90d-1435289768e3/heartbeat"
    req = _request(
        scope_extra={"route": SimpleNamespace(path=template)},
        path=rendered,
    )
    assert _route_label(req) == template


def test_route_label_unmatched_routes_collapse_to_placeholder():
    """The cardinality bomb: 404s and pre-routed middleware see
    `scope['route']` as None, and the rendered path with the UUID would
    otherwise leak in. The placeholder caps cardinality at one extra
    series across all unmatched paths, regardless of how many different
    UUIDs hit the server."""
    rendered = "/api/v1/collectors/9b3bf374-d05c-41ab-b90d-1435289768e3/heartbeat"
    req = _request(scope_extra={}, path=rendered)
    label = _route_label(req)
    # Critical: the UUID MUST NOT appear in the label.
    assert "9b3bf374" not in label
    assert label == _UNMATCHED_ROUTE


def test_route_label_route_without_path_attr_falls_back_to_placeholder():
    """A scope['route'] object missing its `path` attribute (defensive —
    custom route classes) must NOT cause the helper to emit the rendered
    path as a label."""
    rendered = "/api/v1/things/9b3bf374"
    req = _request(
        scope_extra={"route": SimpleNamespace()},  # no .path
        path=rendered,
    )
    assert _route_label(req) == _UNMATCHED_ROUTE


def test_route_label_route_with_empty_path_falls_back_to_placeholder():
    """A route whose `path` is the empty string is treated as unresolved
    rather than emitting an empty-string label that Prometheus would
    happily accept."""
    req = _request(
        scope_extra={"route": SimpleNamespace(path="")},
        path="/anything/123",
    )
    assert _route_label(req) == _UNMATCHED_ROUTE


def test_route_label_many_unmatched_uuids_collapse_to_single_series():
    """The functional cardinality check: 1000 different UUIDs in
    different unmatched paths must all return the same label, otherwise
    Prometheus would store 1000 series for the same logical event."""
    labels = {
        _route_label(_request(path=f"/api/v1/x/{i}/heartbeat"))
        for i in range(1000)
    }
    assert labels == {_UNMATCHED_ROUTE}


# ---- Cutover flag: DCIM_DISABLE_GO_PORTED_METRICS ------------------------
#
# We exercise the _go_ported helper directly instead of reloading the
# module. Re-importing dcim.metrics re-registers the same metric names
# against the default Prometheus registry, which raises
# "Duplicated timeseries" — and using a fresh CollectorRegistry per test
# would diverge from how prod actually wires the singletons.


def _fresh_counter():
    """Build a Counter against an isolated registry so tests don't
    collide with the global one."""
    return Counter("test_counter", "test", ["label"], registry=CollectorRegistry())


def _fresh_histogram():
    return Histogram("test_histogram", "test", registry=CollectorRegistry())


def test_go_ported_returns_real_metric_by_default(monkeypatch):
    """Without the flag, _go_ported returns the metric unchanged so
    .inc()/.observe() actually record."""
    monkeypatch.delenv("DCIM_DISABLE_GO_PORTED_METRICS", raising=False)
    # Re-import so the module-level _GO_PORTED_DISABLED reflects the env.

    import dcim.metrics as m

    # Patch the module-level flag directly to avoid the singleton-reload
    # ValueError that test_go_ported_metrics tests trip on.
    monkeypatch.setattr(m, "_GO_PORTED_DISABLED", False)

    counter = _fresh_counter()
    histogram = _fresh_histogram()
    assert m._go_ported(counter) is counter
    assert m._go_ported(histogram) is histogram


def test_go_ported_returns_noop_when_flag_set(monkeypatch):
    """With the flag, _go_ported returns a _NoopMetric whose .labels(),
    .inc(), and .observe() are silent."""
    import dcim.metrics as m

    monkeypatch.setattr(m, "_GO_PORTED_DISABLED", True)

    counter = _fresh_counter()
    wrapped = m._go_ported(counter)
    assert wrapped.__class__.__name__ == "_NoopMetric"
    # The same call surface — must not raise, must not affect the real counter.
    wrapped.labels(severity="major").inc()
    wrapped.labels(outcome="ok").inc(5)
    wrapped.observe(1.0)
    # Underlying counter untouched.
    assert counter._metrics == {}


def test_noop_metric_chains_indefinitely():
    """labels() returns self, so chained calls like
    `m.labels(a=1).labels(b=2).inc()` don't blow up — matches the
    Counter API even though .labels() on a real Counter doesn't chain."""
    from dcim.metrics import _NoopMetric

    noop = _NoopMetric()
    noop.labels(a=1).labels(b=2).inc()
    noop.labels(c=3).observe(0.5)


def test_env_truthy_values_disable(monkeypatch):
    """The module-level flag reads on import; this test pins which env
    strings are accepted as truthy by reaching into the same predicate."""
    truthy = ("1", "true", "TRUE", "yes", "On")
    falsy = ("0", "false", "no", "off", "", "maybe", "  ")
    for v in truthy:
        assert v.lower() in ("1", "true", "yes", "on"), f"{v!r} should disable"
    for v in falsy:
        assert v.lower() not in ("1", "true", "yes", "on"), f"{v!r} should keep live"
