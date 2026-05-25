"""PR 82 — wiring tests for the template fan-out push.

Pure: the actual SQL JOIN runs against the DB (covered by
integration tests), but we pin the function's contract here —
async signature, returns a list, accepts (db, template_id). If the
shape changes, the API handler that calls it breaks too.
"""

from __future__ import annotations

import inspect

from dcim.services import dhcp_push


def test_schedule_template_fanout_pushes_is_async():
    assert inspect.iscoroutinefunction(dhcp_push.schedule_template_fanout_pushes)


def test_schedule_template_fanout_pushes_signature():
    sig = inspect.signature(dhcp_push.schedule_template_fanout_pushes)
    params = list(sig.parameters)
    assert params == ["db", "template_id"]


def test_schedule_template_fanout_pushes_returns_list_annotation():
    sig = inspect.signature(dhcp_push.schedule_template_fanout_pushes)
    # Forward-ref string in 3.10+ annotations mode; literal list otherwise.
    ann = sig.return_annotation
    assert ann in (list, "list", inspect.Signature.empty) or "list" in str(ann)


def test_auto_push_background_helper_is_exposed_for_handlers():
    # The template update handler calls this directly via
    # background_tasks.add_task. Without the export the wiring breaks.
    assert hasattr(dhcp_push, "auto_push_scope_in_background")
    assert inspect.iscoroutinefunction(dhcp_push.auto_push_scope_in_background)
