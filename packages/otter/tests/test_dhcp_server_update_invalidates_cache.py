"""PR 103 — wiring tests for bundle cache invalidation on
DhcpServer base_config edits.

PR 83 added the pre-render bundle cache (bundle_cache_at/etag/json)
but didn't wire the server-update handler to clear it when
base_config changes. PR 103 fills that gap: editing base_config
must NULL the cache columns inline (so a concurrent GET sees the
fall-through to live render) AND enqueue a re-render so the next
poll lands warm. Other field edits (name, kea_url, auth_*, etc.)
don't touch the rendered Kea config so they don't invalidate.

Pure: introspects the handler signature + source to pin the
behavior. The HTTP path runs against a real DB in integration
tests.
"""

from __future__ import annotations

import inspect

import pytest

# PR 17 cutover: PATCH /dhcp/servers/{id} moved to otter-go. The
# bundle cache invalidation that this test file was pinning happens
# now via the dhcp_bundle_rerender cron (PR #219, runs every 2 min)
# rather than the inline background_tasks enqueue Python used. The
# Go HTTP handler doesn't NULL the cache columns inline because the
# cron rewrites them on the next tick. Skipping all four tests
# below — the invariant they protect no longer applies to the cut-
# over code path.

pytestmark = pytest.mark.skip(
    reason="PR 17 cutover: PATCH /dhcp/servers/{id} on otter-go; "
    "bundle invalidation now via dhcp_bundle_rerender cron",
)


# Stub so the import doesn't crash collection. The pytestmark above
# prevents any test below from running.
class _StubModule:  # pragma: no cover
    def __getattr__(self, _name):
        raise NotImplementedError


ipam = _StubModule()


def test_update_dhcp_server_accepts_background_tasks():
    # BackgroundTasks is needed to schedule the re-render after the
    # response goes out (so the user's PATCH doesn't block on Redis).
    sig = inspect.signature(ipam.update_dhcp_server)
    bg = sig.parameters.get("background_tasks")
    assert bg is not None
    # Annotation may be a forward-ref string under `from __future__
    # import annotations`; match by name for robustness.
    ann = bg.annotation
    name = ann if isinstance(ann, str) else getattr(ann, "__name__", str(ann))
    assert name == "BackgroundTasks"


def test_update_dhcp_server_nulls_cache_columns_on_base_config_edit():
    src = inspect.getsource(ipam.update_dhcp_server)
    # All three columns must be cleared together; an etag without a
    # matching json (or vice versa) would have the bundle GET serve
    # mismatched data.
    assert "bundle_cache_at = None" in src
    assert "bundle_cache_etag = None" in src
    assert "bundle_cache_json = None" in src


def test_update_dhcp_server_guards_invalidation_on_base_config_diff():
    # Only the base_config field changes the rendered bundle. Other
    # fields (name, auth_*, enabled, auto_push, kea_url) leave the
    # JSON content untouched, so invalidating on every PATCH would
    # waste a re-render.
    src = inspect.getsource(ipam.update_dhcp_server)
    assert '"base_config" in diff' in src


def test_update_dhcp_server_enqueues_rerender_after_invalidation():
    # NULLing the cache makes the next GET fall through to live
    # render — correct, but slow. The enqueue warms the cache so the
    # dhcp-site puller's next poll hits the fast path again.
    src = inspect.getsource(ipam.update_dhcp_server)
    assert "enqueue_bundle_rerender" in src
