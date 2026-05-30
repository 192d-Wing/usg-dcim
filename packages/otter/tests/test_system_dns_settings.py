"""Unit tests for the system-wide DNS upstream override.

`get_system_dns_upstreams` is the single seam every renderer call
hits when resolving the catch-all forwarder list. Confirms the
fallback chain (DB override → env-backed default) and that the
DB row's value is normalized on the way out.

The `_normalize_upstreams` helper that used to live in
`dcim.api.admin` was deleted alongside the rest of the Python admin
module when those routes were ported to otter-go; equivalent unit
tests now live in
`packages/otter-go/internal/admin/system_dns_test.go`
(`TestNormalizeUpstreams_*`).
"""

from __future__ import annotations

from types import SimpleNamespace
from typing import Any

import pytest

from dcim.services.dns import (
    _SYSTEM_KEY_DNS_RECURSIVE_UPSTREAMS,
    get_system_dns_upstreams,
)

# ---------- get_system_dns_upstreams ----------

class _FakeSession:
    """Minimal stub: only `get(Model, pk)` is exercised by this helper."""

    def __init__(self, row: Any = None) -> None:
        self._row = row

    async def get(self, _model: Any, _pk: Any) -> Any:
        return self._row


def _row(value: Any) -> SimpleNamespace:
    return SimpleNamespace(
        key=_SYSTEM_KEY_DNS_RECURSIVE_UPSTREAMS,
        value=value,
    )


async def test_get_falls_back_to_env_when_no_row(monkeypatch):
    from dcim.services import dns as dns_svc

    monkeypatch.setattr(
        dns_svc, "get_settings",
        lambda: SimpleNamespace(dns_recursive_upstreams=["1.1.1.1", "8.8.8.8"]),
    )
    out = await get_system_dns_upstreams(_FakeSession(row=None))
    assert out == ["1.1.1.1", "8.8.8.8"]


async def test_get_returns_row_value_when_set(monkeypatch):
    from dcim.services import dns as dns_svc

    monkeypatch.setattr(
        dns_svc, "get_settings",
        lambda: SimpleNamespace(dns_recursive_upstreams=["never", "see", "me"]),
    )
    out = await get_system_dns_upstreams(
        _FakeSession(row=_row(["10.0.0.53", "10.0.0.54"])),
    )
    assert out == ["10.0.0.53", "10.0.0.54"]


async def test_get_falls_back_when_row_value_is_empty_list(monkeypatch):
    """Empty list in the row is treated like 'no override' so the
    renderer never produces a Corefile with zero forward targets."""
    from dcim.services import dns as dns_svc

    monkeypatch.setattr(
        dns_svc, "get_settings",
        lambda: SimpleNamespace(dns_recursive_upstreams=["1.1.1.1"]),
    )
    out = await get_system_dns_upstreams(_FakeSession(row=_row([])))
    assert out == ["1.1.1.1"]


async def test_get_falls_back_when_row_value_is_null(monkeypatch):
    from dcim.services import dns as dns_svc

    monkeypatch.setattr(
        dns_svc, "get_settings",
        lambda: SimpleNamespace(dns_recursive_upstreams=["1.1.1.1"]),
    )
    out = await get_system_dns_upstreams(_FakeSession(row=_row(None)))
    assert out == ["1.1.1.1"]


async def test_get_coerces_row_values_to_str(monkeypatch):
    """JSON columns are duck-typed; defend against an operator who
    pokes a non-string value into the row by hand."""
    from dcim.services import dns as dns_svc

    monkeypatch.setattr(
        dns_svc, "get_settings",
        lambda: SimpleNamespace(dns_recursive_upstreams=[]),
    )
    out = await get_system_dns_upstreams(_FakeSession(row=_row([1, "2"])))
    assert out == ["1", "2"]


_ = pytest  # silence unused-import linter
