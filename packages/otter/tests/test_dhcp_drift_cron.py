"""PR 81 — wiring tests for the dhcp_drift_check arq task.

Pure: doesn't run the task (that needs a live DB + Kea). Confirms
the task is registered, the cron entry exists with a sensible
cadence, and the function is importable + structurally valid.
Integration tests against a real worker live elsewhere.
"""

from __future__ import annotations

import inspect

from dcim import worker


def test_dhcp_drift_check_is_registered_in_worker_functions():
    # Without this entry, the operator can't enqueue the task ad-hoc
    # via `arq dcim.worker.WorkerSettings` for a manual sweep.
    assert worker.dhcp_drift_check in worker.WorkerSettings.functions


def test_dhcp_drift_check_has_a_cron_entry():
    # The cron list carries CronJob objects (one per cron() call).
    # Each carries a .coroutine attribute pointing at the registered
    # task; we walk them to confirm dhcp_drift_check is scheduled.
    coros = [
        getattr(c, "coroutine", None) for c in worker.WorkerSettings.cron_jobs
    ]
    assert worker.dhcp_drift_check in coros


def test_dhcp_drift_check_cron_cadence_runs_four_times_per_hour():
    # 15-minute cadence. If this number changes the operator-doc
    # paragraph in PR 81's commit message goes stale.
    for c in worker.WorkerSettings.cron_jobs:
        if getattr(c, "coroutine", None) is worker.dhcp_drift_check:
            # arq stores the minute set on .minute (set[int]).
            assert len(c.minute) == 4
            break
    else:
        raise AssertionError("dhcp_drift_check cron entry not found")


def test_dhcp_drift_check_is_async_takes_single_ctx():
    sig = inspect.signature(worker.dhcp_drift_check)
    assert inspect.iscoroutinefunction(worker.dhcp_drift_check)
    assert list(sig.parameters) == ["_ctx"]
