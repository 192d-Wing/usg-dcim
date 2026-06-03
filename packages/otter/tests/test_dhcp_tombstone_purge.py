"""PR 100 — wiring tests for the DHCP tombstone purge cron.

Pure: confirms registration + cron cadence + settings knob.
The actual hard-delete runs against a real DB via integration
tests; here we exercise the worker-settings shape and the
configured retention default.
"""

from __future__ import annotations

import inspect

import pytest

from dcim import worker
from dcim.settings import Settings


def test_dhcp_scope_tombstone_purge_is_registered():
    assert worker.dhcp_scope_tombstone_purge in worker.WorkerSettings.functions


@pytest.mark.skip(reason="cron moved to otter-go-scheduler (internal/scheduler/jobs/dhcptombstone)")
def test_dhcp_scope_tombstone_purge_has_a_cron_entry():
    coros = [
        getattr(c, "coroutine", None) for c in worker.WorkerSettings.cron_jobs
    ]
    assert worker.dhcp_scope_tombstone_purge in coros


@pytest.mark.skip(reason="cron moved to otter-go-scheduler (internal/scheduler/jobs/dhcptombstone)")
def test_dhcp_scope_tombstone_purge_runs_once_daily():
    for c in worker.WorkerSettings.cron_jobs:
        if getattr(c, "coroutine", None) is worker.dhcp_scope_tombstone_purge:
            assert c.minute == {30}
            assert c.hour == {3}
            break
    else:
        raise AssertionError("dhcp_scope_tombstone_purge cron entry not found")


def test_settings_default_retention_is_30_days():
    # Lock the default so an accidental config change requires a
    # test update. Operators can override via env var
    # DCIM_DHCP_TOMBSTONE_RETENTION_DAYS.
    s = Settings()
    assert s.dhcp_tombstone_retention_days == 30


def test_dhcp_scope_tombstone_purge_signature_is_async_with_ctx_only():
    sig = inspect.signature(worker.dhcp_scope_tombstone_purge)
    assert inspect.iscoroutinefunction(worker.dhcp_scope_tombstone_purge)
    assert list(sig.parameters) == ["_ctx"]
