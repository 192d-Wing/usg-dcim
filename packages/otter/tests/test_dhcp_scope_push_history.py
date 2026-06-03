"""PR 104 — wiring tests for the per-scope push history table.

Skipped module-wide post-PR #18 (IPAM cutover): the LIST endpoint
asserts depend on dcim.api.ipam which is now an empty stub. The
shape + service-helper tests at the top still describe live behavior
but they share a module-level `from dcim.api import ipam as ipam_api`
which now fails to resolve. Equivalent coverage lives in
internal/dhcp/push/* on otter-go. The original test body is preserved
below behind the module-level skip so a future re-port or parity
audit has the assertions in tree.
"""

from __future__ import annotations

import inspect

import pytest
from sqlalchemy import inspect as sa_inspect

pytestmark = pytest.mark.skip(
    reason="ported to otter-go: internal/dhcp/push/ (PR #18 IPAM cutover)",
)

from dcim.models.ipam import DhcpScopePushHistory  # noqa: E402
from dcim.services import dhcp_push  # noqa: E402

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
# PR 17 cutover: the GET /dhcp/scopes/{id}/push-history route +
# capability + ordering invariants moved to otter-go (see
# internal/ipam/dhcp_push.go + TestPushHistory_DefaultLimit /
# TestPushHistory_Success on the Go side). The originating route-
# registration / cap / order tests are dropped here because the
# dcim.api.ipam stub no longer carries the routes — equivalent
# coverage lives in the otter-go handler tests.
