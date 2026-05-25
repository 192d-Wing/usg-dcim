"""PR 104 — wiring tests for the per-scope push history table.

Pure: pins the model shape, the service helper contract, and the
LIST endpoint registration. The actual INSERT path runs against
a real DB in integration tests (push_scope mutates DB state) so
here we exercise the surface that doesn't need a live session.
"""

from __future__ import annotations

import inspect

from sqlalchemy import inspect as sa_inspect

from dcim.api import ipam as ipam_api
from dcim.models.ipam import DhcpScopePushHistory
from dcim.services import dhcp_push


# ----- model shape -----

def test_push_history_table_name():
    # Matches migration 0064; renaming either side without the other
    # would break first-boot.
    assert DhcpScopePushHistory.__tablename__ == "dhcp_scope_push_history"


def test_push_history_columns_present():
    cols = {c.name for c in sa_inspect(DhcpScopePushHistory).columns}
    # Append-only shape: id + the attempt details + attempted_at.
    # No created_at/updated_at (Timestamped mixin) because the row
    # is immutable once written.
    expected = {
        "id", "scope_id", "server_id", "operation", "kea_subnet_id",
        "status", "error", "duration_ms", "attempted_at",
    }
    assert expected.issubset(cols)


def test_push_history_has_no_updated_at():
    # Append-only — updated_at would be misleading. If a future
    # refactor pulls Timestamped onto the model, this test fails
    # loudly so the migration + invariants get re-evaluated.
    cols = {c.name for c in sa_inspect(DhcpScopePushHistory).columns}
    assert "updated_at" not in cols


# ----- service helper -----

def test_record_push_history_helper_is_async():
    # _record_push_history runs inside push_scope and
    # delete_scope_from_kea; both are async, so the helper must
    # await (db.flush()) inside the same transaction.
    assert inspect.iscoroutinefunction(dhcp_push._record_push_history)


def test_record_push_history_signature():
    # Pins the call shape (db, scope, server, operation, status,
    # error, duration_ms). If a caller drifts off this, the
    # mismatched positional args would silently misalign columns.
    sig = inspect.signature(dhcp_push._record_push_history)
    assert list(sig.parameters) == [
        "db", "scope", "server", "operation", "status", "error", "duration_ms",
    ]


def test_push_scope_calls_record_push_history():
    # Behavioral pin: push_scope must record the attempt on both
    # the transport-error branch and the post-response branch.
    src = inspect.getsource(dhcp_push.push_scope)
    # Recorded on success/error after Kea responds, and on
    # transport-error before the optimistic-id rollback.
    assert src.count("_record_push_history(") >= 2


def test_delete_scope_from_kea_calls_record_push_history():
    # The DELETE handler passes db=db so the row gets written;
    # callers that don't have a session (PR 74 backwards-compat)
    # still work because db is keyword-optional.
    src = inspect.getsource(dhcp_push.delete_scope_from_kea)
    assert "_record_push_history(" in src


def test_delete_scope_from_kea_accepts_optional_db():
    sig = inspect.signature(dhcp_push.delete_scope_from_kea)
    db_param = sig.parameters.get("db")
    assert db_param is not None
    # default=None keeps PR 74 callers working without changes.
    assert db_param.default is None


# ----- API endpoint registration -----

def test_list_push_history_endpoint_registered():
    # GET /dhcp/scopes/{scope_id}/push-history must be on the
    # router; absent registration would route to 404 silently.
    paths = {
        r.path for r in ipam_api.router.routes if hasattr(r, "path")
    }
    assert "/ipam/dhcp/scopes/{scope_id}/push-history" in paths


def test_list_push_history_uses_read_capability():
    # Same capability as GET /dhcp/scopes/{id} — operators who
    # can see a scope can see its push history; mutating
    # capabilities aren't required.
    src = inspect.getsource(ipam_api.list_dhcp_scope_push_history)
    assert 'ipam:dhcp-scopes:read' in src


def test_list_push_history_orders_newest_first():
    # The (scope_id, attempted_at DESC) index in migration 0064
    # only pays off if the query asks for DESC. Pin the order so
    # a future refactor doesn't quietly invalidate the index plan.
    src = inspect.getsource(ipam_api.list_dhcp_scope_push_history)
    assert "attempted_at.desc()" in src
