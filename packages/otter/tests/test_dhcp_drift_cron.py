"""PR 81 — wiring tests for the dhcp_drift_check arq task.

Pure: doesn't run the task (that needs a live DB + Kea). Confirms
the task is registered, the cron entry exists with a sensible
cadence, and the function is importable + structurally valid.
Integration tests against a real worker live elsewhere.
"""

from __future__ import annotations

import inspect

import pytest

from dcim import worker


def test_dhcp_drift_check_is_registered_in_worker_functions():
    # Without this entry, the operator can't enqueue the task ad-hoc
    # via `arq dcim.worker.WorkerSettings` for a manual sweep.
    assert worker.dhcp_drift_check in worker.WorkerSettings.functions


@pytest.mark.skip(reason="cron moved to otter-go-scheduler (internal/scheduler/jobs/dhcpdriftcheck)")
def test_dhcp_drift_check_has_a_cron_entry():
    coros = [
        getattr(c, "coroutine", None) for c in worker.WorkerSettings.cron_jobs
    ]
    assert worker.dhcp_drift_check in coros


@pytest.mark.skip(reason="cron moved to otter-go-scheduler (internal/scheduler/jobs/dhcpdriftcheck)")
def test_dhcp_drift_check_cron_cadence_runs_four_times_per_hour():
    for c in worker.WorkerSettings.cron_jobs:
        if getattr(c, "coroutine", None) is worker.dhcp_drift_check:
            assert len(c.minute) == 4
            break
    else:
        raise AssertionError("dhcp_drift_check cron entry not found")


def test_dhcp_drift_check_is_async_takes_single_ctx():
    sig = inspect.signature(worker.dhcp_drift_check)
    assert inspect.iscoroutinefunction(worker.dhcp_drift_check)
    assert list(sig.parameters) == ["_ctx"]
