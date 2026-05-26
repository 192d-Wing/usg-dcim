"""PR 95 — tests for per-scope auto_push_override + soft-delete gate.

Pure: pins the should_auto_push override matrix and the
soft-delete gate (a tombstoned scope never auto-pushes regardless
of override). The DELETE/restore endpoints touch the DB and are
covered by integration tests; here we exercise the in-memory
decision logic.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import UTC, datetime

from dcim.services.dhcp_push import should_auto_push


@dataclass
class _Server:
    enabled: bool = True
    auto_push: bool = False


@dataclass
class _Scope:
    enabled: bool = True
    auto_push_override: bool | None = None
    deleted_at: object = None


# ----- override matrix (PR 95) -----

def test_server_off_override_none_does_not_push():
    assert should_auto_push(_Server(auto_push=False),
                            _Scope(auto_push_override=None)) is False


def test_server_off_override_true_forces_push():
    # Per-scope force overrides the server-wide off setting.
    assert should_auto_push(_Server(auto_push=False),
                            _Scope(auto_push_override=True)) is True


def test_server_off_override_false_does_not_push():
    # Explicit no, explicitly inert (suppress with redundant signal).
    assert should_auto_push(_Server(auto_push=False),
                            _Scope(auto_push_override=False)) is False


def test_server_on_override_none_inherits_push():
    assert should_auto_push(_Server(auto_push=True),
                            _Scope(auto_push_override=None)) is True


def test_server_on_override_true_still_pushes():
    assert should_auto_push(_Server(auto_push=True),
                            _Scope(auto_push_override=True)) is True


def test_server_on_override_false_suppresses_push():
    # Operator wants the server-wide auto-push on but THIS scope
    # paused — common for scopes in maintenance.
    assert should_auto_push(_Server(auto_push=True),
                            _Scope(auto_push_override=False)) is False


# ----- soft-delete gate (PR 95) -----

def test_soft_deleted_scope_never_auto_pushes_even_with_force_override():
    # A tombstone wins over any override — tombstoned scopes don't
    # exist for operational purposes.
    s = _Scope(
        auto_push_override=True,
        deleted_at=datetime.now(UTC),
    )
    assert should_auto_push(_Server(auto_push=True), s) is False


def test_soft_deleted_scope_skipped_even_when_server_pushes():
    s = _Scope(
        auto_push_override=None,
        deleted_at=datetime.now(UTC),
    )
    assert should_auto_push(_Server(auto_push=True), s) is False


def test_live_scope_with_null_deleted_at_still_pushes():
    s = _Scope(auto_push_override=None, deleted_at=None)
    assert should_auto_push(_Server(auto_push=True), s) is True


# ----- existing gates still apply -----

def test_disabled_server_still_wins_over_force_override():
    # A disabled server is unreachable; pushing makes no sense even
    # if the operator forced the override on.
    assert should_auto_push(
        _Server(enabled=False, auto_push=True),
        _Scope(auto_push_override=True),
    ) is False


def test_disabled_scope_blocks_even_with_force_override():
    # Disabled scopes shouldn't be in Kea; forcing the override
    # would push a disabled-state scope, contradicting itself.
    assert should_auto_push(
        _Server(auto_push=True),
        _Scope(enabled=False, auto_push_override=True),
    ) is False


# ----- schema knob -----

def test_dhcp_scope_schema_carries_auto_push_override_default_null():
    from uuid import uuid4

    from dcim.schemas.ipam import DhcpScopeCreate
    payload = DhcpScopeCreate(
        dhcp_server_id=uuid4(),
        name="lab-v4", ip_family=4, prefix="10.0.0.0/24",
        pools=[{"first": "10.0.0.10", "last": "10.0.0.250"}],
    )
    # Operator must opt in explicitly — default is inherit
    # (None == NULL on the column).
    assert payload.auto_push_override is None


def test_dhcp_scope_update_schema_accepts_explicit_null_to_clear():
    from dcim.schemas.ipam import DhcpScopeUpdate
    # exclude_unset means {"auto_push_override": None} survives —
    # that's how the operator clears a previously-set override
    # back to inherit-from-server.
    u = DhcpScopeUpdate(auto_push_override=None)
    diff = u.model_dump(exclude_unset=True)
    assert "auto_push_override" in diff
    assert diff["auto_push_override"] is None
