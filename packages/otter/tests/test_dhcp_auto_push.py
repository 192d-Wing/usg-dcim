"""Unit tests for auto-push dispatch gate (PR 79).

Pure: pins the `should_auto_push` decision in isolation. The actual
background execution (`auto_push_scope_in_background`) is exercised
by integration tests against a live Kea instance — out of scope for
this suite.
"""

from __future__ import annotations

from dataclasses import dataclass

from dcim.security.capabilities import CAPABILITY_CATALOG
from dcim.services.dhcp_push import should_auto_push


@dataclass
class _Server:
    enabled: bool = True
    auto_push: bool = False


@dataclass
class _Scope:
    enabled: bool = True


# ----- gate decisions -----

def test_auto_push_off_skips_dispatch():
    # Default: server.auto_push is False — never schedule.
    assert should_auto_push(_Server(auto_push=False), _Scope()) is False


def test_auto_push_on_with_enabled_server_and_scope_dispatches():
    assert should_auto_push(_Server(auto_push=True), _Scope()) is True


def test_disabled_server_blocks_auto_push():
    # A disabled DhcpServer shouldn't talk to Kea at all — auto_push
    # respects the server's own enabled flag.
    assert should_auto_push(
        _Server(enabled=False, auto_push=True), _Scope(),
    ) is False


def test_disabled_scope_blocks_auto_push():
    # Disabled scopes are omitted from the bundle; pushing them via
    # subnet_cmds would contradict that. Operator flips enabled=True
    # to bring it into Kea.
    assert should_auto_push(
        _Server(auto_push=True), _Scope(enabled=False),
    ) is False


def test_none_server_is_a_no_op():
    # Defensive: the API handler always loads the server, but the
    # gate should fail closed if the caller passes None.
    assert should_auto_push(None, _Scope()) is False


def test_none_scope_is_a_pass_when_server_allows():
    # Some call sites may want to schedule a server-level push (e.g.
    # template update → re-push all scopes). The gate treats no
    # scope as "no scope-level objection."
    assert should_auto_push(_Server(auto_push=True), None) is True


def test_none_scope_with_auto_push_off_still_skips():
    assert should_auto_push(_Server(auto_push=False), None) is False


# ----- schema knob -----

def test_dhcp_server_schema_carries_auto_push_with_default_false():
    from uuid import uuid4

    from dcim.schemas.ipam import DhcpServerCreate
    payload = DhcpServerCreate(
        name="kea-east-1", fabric_id=uuid4(),
        kea_url="https://kea.east.example.mil:8000",
    )
    # Operator must opt in explicitly; status quo from PR 74 is the
    # default for any existing deployment.
    assert payload.auto_push is False


def test_dhcp_server_schema_accepts_auto_push_true():
    from uuid import uuid4

    from dcim.schemas.ipam import DhcpServerCreate
    payload = DhcpServerCreate(
        name="kea-east-1", fabric_id=uuid4(),
        kea_url="https://kea.east.example.mil:8000",
        auto_push=True,
    )
    assert payload.auto_push is True


# ----- capabilities — auto-push doesn't add a new one -----

def test_auto_push_reuses_existing_dhcp_scopes_push_capability():
    # No new capability for auto-push. The handler's :create or
    # :update permission already gates the mutation; the background
    # task runs as the server itself, not on behalf of a principal,
    # so no further check is needed.
    actions = set(CAPABILITY_CATALOG["ipam"]["dhcp-scopes"])
    assert "push" in actions
    assert "auto-push" not in actions  # explicitly NOT a new code
