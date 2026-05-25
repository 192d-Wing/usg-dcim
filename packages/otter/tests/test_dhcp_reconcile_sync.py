"""PR 85 — tests for the mutating reconcile sync.

The full path runs against a real DB via integration tests; here we
pin the decision matrix using a fake AsyncSession that captures
inserts and lets us assert on the SyncReport's counts and entries.
"""

from __future__ import annotations

import asyncio
from dataclasses import dataclass, field
from uuid import UUID, uuid4

from dcim.models.ipam import IpAddressSource
from dcim.services.dhcp_reconcile import sync_reservations


@dataclass
class _Src:
    value: str


@dataclass
class _IPRow:
    id: UUID
    address: str
    source: _Src
    status: object = None


@dataclass
class _Scope:
    id: UUID
    subnet_id: UUID | None
    reservations_json: list = field(default_factory=list)


class _FakeSession:
    """Minimal AsyncSession stand-in: records adds and flushes."""

    def __init__(self):
        self.added: list = []
        self.flushed = 0

    def add(self, obj):
        self.added.append(obj)

    async def flush(self):
        self.flushed += 1


def _ip(addr: str, source: str) -> _IPRow:
    return _IPRow(id=uuid4(), address=addr, source=_Src(source))


# Sync wrappers around the async coroutine. The codebase's existing
# test_kea / test_dhcp_push patterns use bare `async def` tests too
# but those are skipped in this venv when pytest-asyncio is missing;
# we use asyncio.run() directly so the suite passes regardless.


def _run(coro):
    return asyncio.run(coro)


# ----- unbacked → upsert -----

def test_unbacked_reservation_creates_new_ipaddress_row():
    scope = _Scope(
        id=uuid4(), subnet_id=uuid4(),
        reservations_json=[{"mac": "aa:bb:cc:dd:ee:ff", "ip": "10.0.0.99"}],
    )
    sess = _FakeSession()
    report = _run(sync_reservations(sess, scope, []))
    assert report.upserted == 1
    assert report.promoted == 0
    assert len(sess.added) == 1
    new_row = sess.added[0]
    assert str(new_row.address) == "10.0.0.99"
    assert new_row.source == IpAddressSource.reservation


# ----- dhcp source → promote -----

def test_dhcp_source_row_gets_promoted_to_reservation():
    scope = _Scope(
        id=uuid4(), subnet_id=uuid4(),
        reservations_json=[{"mac": "aa:bb:cc:dd:ee:ff", "ip": "10.0.0.5"}],
    )
    sess = _FakeSession()
    dhcp_row = _ip("10.0.0.5", IpAddressSource.dhcp.value)
    report = _run(sync_reservations(sess, scope, [dhcp_row]))
    assert report.promoted == 1
    assert report.upserted == 0
    assert dhcp_row.source == IpAddressSource.reservation


# ----- static source → skipped collision -----

def test_static_collision_is_skipped_and_row_unchanged():
    scope = _Scope(
        id=uuid4(), subnet_id=uuid4(),
        reservations_json=[{"mac": "aa:bb:cc:dd:ee:ff", "ip": "10.0.0.5"}],
    )
    sess = _FakeSession()
    static_row = _ip("10.0.0.5", IpAddressSource.static.value)
    report = _run(sync_reservations(sess, scope, [static_row]))
    assert report.skipped_collision == 1
    # _Src wrapper mimics the enum's .value access; the service
    # doesn't mutate static-source rows, so the wrapper survives.
    assert static_row.source.value == IpAddressSource.static.value


# ----- already reservation → skipped clean -----

def test_reservation_source_is_left_alone():
    scope = _Scope(
        id=uuid4(), subnet_id=uuid4(),
        reservations_json=[{"mac": "aa:bb:cc:dd:ee:ff", "ip": "10.0.0.5"}],
    )
    sess = _FakeSession()
    res_row = _ip("10.0.0.5", IpAddressSource.reservation.value)
    report = _run(sync_reservations(sess, scope, [res_row]))
    assert report.skipped_clean == 1
    assert report.upserted == 0
    assert report.promoted == 0


# ----- no subnet → skip everything -----

def test_scope_without_subnet_skips_all_reservations():
    scope = _Scope(
        id=uuid4(), subnet_id=None,
        reservations_json=[
            {"mac": "aa:bb:cc:dd:ee:01", "ip": "10.0.0.5"},
            {"mac": "aa:bb:cc:dd:ee:02", "ip": "10.0.0.6"},
        ],
    )
    sess = _FakeSession()
    report = _run(sync_reservations(sess, scope, []))
    assert report.skipped_no_subnet == 2
    assert report.upserted == 0
    assert len(sess.added) == 0


# ----- mixed report -----

def test_mixed_reservation_set_aggregates_into_per_decision_counts():
    scope = _Scope(
        id=uuid4(), subnet_id=uuid4(),
        reservations_json=[
            {"mac": "aa:bb:cc:dd:ee:01", "ip": "10.0.0.5"},   # promote (dhcp)
            {"mac": "aa:bb:cc:dd:ee:02", "ip": "10.0.0.10"},  # skip collision
            {"mac": "aa:bb:cc:dd:ee:03", "ip": "10.0.0.99"},  # upsert
            {"mac": "aa:bb:cc:dd:ee:04", "ip": "10.0.0.20"},  # skip clean
        ],
    )
    sess = _FakeSession()
    rows = [
        _ip("10.0.0.5", IpAddressSource.dhcp.value),
        _ip("10.0.0.10", IpAddressSource.static.value),
        _ip("10.0.0.20", IpAddressSource.reservation.value),
    ]
    report = _run(sync_reservations(sess, scope, rows))
    assert report.upserted == 1
    assert report.promoted == 1
    assert report.skipped_collision == 1
    assert report.skipped_clean == 1


# ----- capability registered -----

def test_dhcp_scopes_reconcile_sync_capability_registered():
    from dcim.security.capabilities import CAPABILITY_CATALOG
    actions = set(CAPABILITY_CATALOG["ipam"]["dhcp-scopes"])
    assert "reconcile-sync" in actions
