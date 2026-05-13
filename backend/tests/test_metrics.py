"""Unit tests for the Prometheus metrics middleware.

The biggest production risk in the metrics layer is **cardinality
explosion** — a label that contains a UUID or numeric ID creates one
time series per unique value, and Prometheus storage scales poorly past
~10k series per metric. The tests below lock the route-label contract
against regressions that would re-introduce that failure mode.
"""

from types import SimpleNamespace

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
