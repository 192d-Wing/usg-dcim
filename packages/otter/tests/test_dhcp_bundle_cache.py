"""PR 83 — wiring tests for the bundle pre-render cache.

Pure: pins the worker task registration, the enqueue helper's
contract, and the schema columns. The actual cache write path runs
under arq + a live DB — integration test territory, not here.
"""

from __future__ import annotations

import inspect

from dcim import worker
from dcim.services import dhcp_push

# ----- worker task registration -----

def test_rerender_dhcp_bundle_is_registered_in_worker_functions():
    # Without this, `pool.enqueue_job("rerender_dhcp_bundle", ...)`
    # fails at runtime — the API enqueue would silently land in the
    # arq logs with "function not found."
    assert worker.rerender_dhcp_bundle in worker.WorkerSettings.functions


def test_rerender_dhcp_bundle_has_no_cron_entry():
    # Designed as on-demand, not scheduled. A cron entry would defeat
    # the etag-cache point (we'd re-render on a timer even when
    # nothing changed).
    coros = [
        getattr(c, "coroutine", None) for c in worker.WorkerSettings.cron_jobs
    ]
    assert worker.rerender_dhcp_bundle not in coros


def test_rerender_dhcp_bundle_signature_takes_server_id():
    sig = inspect.signature(worker.rerender_dhcp_bundle)
    # arq passes ctx as first arg; second is the user arg.
    params = list(sig.parameters)
    assert params == ["_ctx", "server_id"]


# ----- enqueue helper -----

def test_enqueue_bundle_rerender_is_async_and_takes_server_id():
    assert inspect.iscoroutinefunction(dhcp_push.enqueue_bundle_rerender)
    sig = inspect.signature(dhcp_push.enqueue_bundle_rerender)
    assert list(sig.parameters) == ["server_id"]


def test_enqueue_bundle_rerender_returns_bool_annotation():
    # Best-effort: returns False on Redis failure, True on enqueue
    # success. The handler doesn't act on the return — but the
    # annotation pins the contract for future callers.
    sig = inspect.signature(dhcp_push.enqueue_bundle_rerender)
    assert sig.return_annotation is bool or sig.return_annotation == "bool"


# ----- model exposes cache columns -----

def test_dhcp_server_model_has_bundle_cache_columns():
    from dcim.models.ipam import DhcpServer
    assert hasattr(DhcpServer, "bundle_cache_at")
    assert hasattr(DhcpServer, "bundle_cache_etag")
    assert hasattr(DhcpServer, "bundle_cache_json")
