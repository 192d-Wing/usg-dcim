"""Unit tests for the DHCP reservation ↔ IPAddress reconcile (PR 84).

Pure: no DB, no HTTP. Pins per-reservation status decisions
(clean / collision / unbacked), the v4-mac vs v6-duid identifier
handling, the IP normalization, and the no-subnet edge case.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from uuid import UUID, uuid4

from dcim.models.ipam import IpAddressSource
from dcim.services.dhcp_reconcile import (
    _normalize_ip,
    reconcile_scope,
)


@dataclass
class _IPRow:
    id: UUID
    address: str
    source: object  # IpAddressSource value or enum


def _ip(addr: str, source: str) -> _IPRow:
    return _IPRow(id=uuid4(), address=addr, source=_Src(source))


@dataclass
class _Src:
    value: str  # mimic enum.value


@dataclass
class _Scope:
    id: UUID
    subnet_id: UUID | None = uuid4()
    reservations_json: list = field(default_factory=list)


# ----- _normalize_ip helper -----

def test_normalize_strips_whitespace_and_canonicalizes_v4():
    assert _normalize_ip("  10.0.0.5  ") == "10.0.0.5"


def test_normalize_canonicalizes_v6_shorthand():
    # 2001:db8:0:0:0:0:0:5 → 2001:db8::5 after canonicalization.
    assert _normalize_ip("2001:db8:0:0:0:0:0:5") == "2001:db8::5"


def test_normalize_rejects_garbage_returns_none():
    assert _normalize_ip("not-an-ip") is None
    assert _normalize_ip("") is None


# ----- clean: reservation matches a source=reservation/dhcp IPAddress -----

def test_reservation_matching_dhcp_source_is_clean():
    # PR 84: source=dhcp counts as clean — the lease has materialized.
    # PR 88: clean requires the MAC to agree (or one side to be NULL);
    # this fixture leaves dhcp_mac unset so the check is bypassed.
    scope = _Scope(
        id=uuid4(),
        reservations_json=[{"mac": "aa:bb:cc:dd:ee:ff", "ip": "10.0.0.5"}],
    )
    rows = [_ip("10.0.0.5", IpAddressSource.dhcp.value)]
    report = reconcile_scope(scope, rows)
    assert report.counts == {
        "clean": 1, "collision": 0, "unbacked": 0,
        "mac_mismatch": 0, "duid_mismatch": 0,
    }
    assert report.entries[0].status == "clean"


def test_reservation_matching_reservation_source_is_clean():
    scope = _Scope(
        id=uuid4(),
        reservations_json=[{"mac": "aa:bb:cc:dd:ee:ff", "ip": "10.0.0.5"}],
    )
    rows = [_ip("10.0.0.5", IpAddressSource.reservation.value)]
    report = reconcile_scope(scope, rows)
    assert report.entries[0].status == "clean"


# ----- collision: reservation IP is source=static -----

def test_reservation_against_static_is_collision():
    scope = _Scope(
        id=uuid4(),
        reservations_json=[{"mac": "aa:bb:cc:dd:ee:ff", "ip": "10.0.0.5"}],
    )
    rows = [_ip("10.0.0.5", IpAddressSource.static.value)]
    report = reconcile_scope(scope, rows)
    assert report.counts["collision"] == 1
    entry = report.entries[0]
    assert entry.status == "collision"
    assert entry.ip_source == IpAddressSource.static.value
    assert "static" in (entry.note or "")


# ----- unbacked: no matching IPAddress -----

def test_reservation_with_no_matching_ipaddress_is_unbacked():
    scope = _Scope(
        id=uuid4(),
        reservations_json=[{"mac": "aa:bb:cc:dd:ee:ff", "ip": "10.0.0.99"}],
    )
    rows = [_ip("10.0.0.5", IpAddressSource.dhcp.value)]
    report = reconcile_scope(scope, rows)
    assert report.counts["unbacked"] == 1
    assert report.entries[0].status == "unbacked"


def test_reservation_with_unparseable_ip_is_unbacked_with_note():
    scope = _Scope(
        id=uuid4(),
        reservations_json=[{"mac": "aa:bb:cc:dd:ee:ff", "ip": "not-an-ip"}],
    )
    report = reconcile_scope(scope, [])
    assert report.entries[0].status == "unbacked"
    assert "parseable" in (report.entries[0].note or "")


# ----- scope without subnet_id: cross-check skipped -----

def test_no_subnet_id_yields_unbacked_with_explanatory_note():
    scope = _Scope(
        id=uuid4(), subnet_id=None,
        reservations_json=[{"mac": "aa:bb:cc:dd:ee:ff", "ip": "10.0.0.5"}],
    )
    report = reconcile_scope(scope, [])
    assert report.subnet_id is None
    assert report.entries[0].status == "unbacked"
    assert "subnet" in (report.entries[0].note or "")


# ----- identifier passthrough: v4 mac, v6 duid -----

def test_v6_reservation_uses_duid_identifier():
    scope = _Scope(
        id=uuid4(),
        reservations_json=[{"duid": "00:01:00:01:abc", "ip": "2001:db8::5"}],
    )
    rows = [_ip("2001:db8::5", IpAddressSource.dhcp.value)]
    report = reconcile_scope(scope, rows)
    assert report.entries[0].identifier == "00:01:00:01:abc"


def test_v4_reservation_uses_mac_identifier():
    scope = _Scope(
        id=uuid4(),
        reservations_json=[{"mac": "aa:bb:cc:dd:ee:ff", "ip": "10.0.0.5"}],
    )
    rows = [_ip("10.0.0.5", IpAddressSource.dhcp.value)]
    report = reconcile_scope(scope, rows)
    assert report.entries[0].identifier == "aa:bb:cc:dd:ee:ff"


# ----- mixed report -----

def test_mixed_states_aggregate_correctly_in_counts():
    scope = _Scope(
        id=uuid4(),
        reservations_json=[
            {"mac": "aa:bb:cc:dd:ee:01", "ip": "10.0.0.5"},   # clean (dhcp)
            {"mac": "aa:bb:cc:dd:ee:02", "ip": "10.0.0.10"},  # collision
            {"mac": "aa:bb:cc:dd:ee:03", "ip": "10.0.0.99"},  # unbacked
        ],
    )
    rows = [
        _ip("10.0.0.5", IpAddressSource.dhcp.value),
        _ip("10.0.0.10", IpAddressSource.static.value),
    ]
    report = reconcile_scope(scope, rows)
    assert report.total == 3
    assert report.counts == {
        "clean": 1, "collision": 1, "unbacked": 1,
        "mac_mismatch": 0, "duid_mismatch": 0,
    }


# ----- IP normalization across the index -----

def test_short_v6_form_in_scope_matches_canonical_form_in_ipam():
    # Scope stores "2001:db8::5"; IPAM stores "2001:db8:0:0:0:0:0:5".
    # Both normalize the same way so the match succeeds.
    scope = _Scope(
        id=uuid4(),
        reservations_json=[{"duid": "duid-x", "ip": "2001:db8::5"}],
    )
    rows = [_ip("2001:db8:0:0:0:0:0:5", IpAddressSource.reservation.value)]
    report = reconcile_scope(scope, rows)
    assert report.entries[0].status == "clean"


# ----- capability registration -----

def test_dhcp_scopes_reconcile_capability_registered():
    from dcim.security.capabilities import CAPABILITY_CATALOG
    assert "reconcile" in CAPABILITY_CATALOG["ipam"]["dhcp-scopes"]


# ----- PR 88: MAC binding -----

def _ip_with_mac(addr: str, source: str, mac: str | None) -> _IPRow:
    row = _IPRow(id=uuid4(), address=addr, source=_Src(source))
    row.dhcp_mac = mac
    return row


def test_normalize_mac_canonicalizes_separators_and_case():
    from dcim.services.dhcp_reconcile import _normalize_mac
    # Colon, hyphen, dot, no-separator — all canonicalize the same way.
    expected = "aa:bb:cc:dd:ee:ff"
    assert _normalize_mac("AA:BB:CC:DD:EE:FF") == expected
    assert _normalize_mac("aa-bb-cc-dd-ee-ff") == expected
    assert _normalize_mac("aabb.ccdd.eeff") == expected
    assert _normalize_mac("aabbccddeeff") == expected


def test_normalize_mac_rejects_wrong_length():
    from dcim.services.dhcp_reconcile import _normalize_mac
    assert _normalize_mac("aa:bb:cc") is None
    assert _normalize_mac("") is None
    assert _normalize_mac(None) is None


def test_reservation_with_matching_mac_is_clean():
    scope = _Scope(
        id=uuid4(),
        reservations_json=[{"mac": "AA:BB:CC:DD:EE:FF", "ip": "10.0.0.5"}],
    )
    rows = [_ip_with_mac("10.0.0.5", IpAddressSource.dhcp.value, "aa-bb-cc-dd-ee-ff")]
    report = reconcile_scope(scope, rows)
    # Case + separator differ; normalize_mac canonicalizes both sides.
    assert report.counts["clean"] == 1
    assert report.counts["mac_mismatch"] == 0


def test_reservation_with_mismatched_mac_is_mac_mismatch():
    scope = _Scope(
        id=uuid4(),
        reservations_json=[{"mac": "aa:bb:cc:dd:ee:ff", "ip": "10.0.0.5"}],
    )
    rows = [_ip_with_mac("10.0.0.5", IpAddressSource.dhcp.value, "11:22:33:44:55:66")]
    report = reconcile_scope(scope, rows)
    assert report.counts["mac_mismatch"] == 1
    assert report.counts["clean"] == 0
    entry = report.entries[0]
    assert entry.status == "mac_mismatch"
    assert "aa:bb:cc:dd:ee:ff" in (entry.note or "")
    assert "11:22:33:44:55:66" in (entry.note or "")


def test_reservation_without_mac_skips_the_check_entirely():
    # v6 reservations use duid, not mac. The check is no-op when
    # either side lacks a MAC — falls through to clean as before.
    scope = _Scope(
        id=uuid4(),
        reservations_json=[{"duid": "00:01:00:01:abcd", "ip": "10.0.0.5"}],
    )
    rows = [_ip_with_mac("10.0.0.5", IpAddressSource.dhcp.value, "11:22:33:44:55:66")]
    report = reconcile_scope(scope, rows)
    assert report.counts["clean"] == 1
    assert report.counts["mac_mismatch"] == 0


def test_ipaddress_without_mac_skips_the_check():
    # Lease ingested before mac was known — IPAddress.dhcp_mac is
    # NULL. Reservation supplied a mac, but we can't compare; treat
    # as clean (don't false-alarm on missing data).
    scope = _Scope(
        id=uuid4(),
        reservations_json=[{"mac": "aa:bb:cc:dd:ee:ff", "ip": "10.0.0.5"}],
    )
    rows = [_ip_with_mac("10.0.0.5", IpAddressSource.dhcp.value, None)]
    report = reconcile_scope(scope, rows)
    assert report.counts["clean"] == 1
    assert report.counts["mac_mismatch"] == 0


# ----- PR 94: DUID binding (v6 reservations) -----

def _ip_with_duid(addr: str, source: str, duid: str | None) -> _IPRow:
    row = _IPRow(id=uuid4(), address=addr, source=_Src(source))
    row.dhcp_mac = None
    row.dhcp_duid = duid
    return row


def test_normalize_duid_canonicalizes_separators_and_length_range():
    from dcim.services.dhcp_reconcile import _normalize_duid
    # Common 14-byte DUID-LL form — separators differ; canonicalize.
    expected = "00:01:00:01:25:ab:cd:01:aa:bb:cc:dd:ee:ff"
    assert _normalize_duid("00:01:00:01:25:AB:CD:01:AA:BB:CC:DD:EE:FF") == expected
    assert _normalize_duid("0001000125abcd01aabbccddeeff") == expected


def test_normalize_duid_rejects_out_of_range():
    from dcim.services.dhcp_reconcile import _normalize_duid
    # < 2 hex (RFC 8415 minimum 1 byte) or > 256 hex (>128 octets) rejected.
    assert _normalize_duid("") is None
    assert _normalize_duid(None) is None
    assert _normalize_duid("a") is None  # odd length
    assert _normalize_duid("a" * 258) is None  # too long


def test_v6_reservation_with_matching_duid_is_clean():
    # Separator/case differ across the reservation and the IPAddress
    # row; _normalize_duid canonicalizes both sides so the comparison
    # holds.
    scope = _Scope(
        id=uuid4(),
        reservations_json=[{"duid": "00:01:00:01:abcd", "ip": "2001:db8::5"}],
    )
    rows = [_ip_with_duid("2001:db8::5", IpAddressSource.dhcp.value, "0001-0001-ABCD")]
    report = reconcile_scope(scope, rows)
    assert report.counts["clean"] == 1
    assert report.counts["duid_mismatch"] == 0


def test_v6_reservation_with_mismatched_duid_is_duid_mismatch():
    scope = _Scope(
        id=uuid4(),
        reservations_json=[{"duid": "00:01:00:01:abcd", "ip": "2001:db8::5"}],
    )
    rows = [_ip_with_duid("2001:db8::5", IpAddressSource.dhcp.value, "00:01:00:02:eeff")]
    report = reconcile_scope(scope, rows)
    assert report.counts["duid_mismatch"] == 1
    assert report.counts["clean"] == 0
    entry = report.entries[0]
    assert entry.status == "duid_mismatch"
    assert "00:01:00:01:ab:cd" in (entry.note or "")
    assert "00:01:00:02:ee:ff" in (entry.note or "")


def test_v6_reservation_without_duid_skips_the_check():
    # Some v6 reservations key on a different identifier; if no DUID
    # is set, the check is no-op (falls through to clean).
    scope = _Scope(
        id=uuid4(),
        reservations_json=[{"ip": "2001:db8::5"}],
    )
    rows = [_ip_with_duid("2001:db8::5", IpAddressSource.dhcp.value, "00:01:abcd")]
    report = reconcile_scope(scope, rows)
    assert report.counts["clean"] == 1
    assert report.counts["duid_mismatch"] == 0


def test_v6_ipaddress_without_duid_skips_the_check():
    scope = _Scope(
        id=uuid4(),
        reservations_json=[{"duid": "00:01:00:01:abcd", "ip": "2001:db8::5"}],
    )
    rows = [_ip_with_duid("2001:db8::5", IpAddressSource.dhcp.value, None)]
    report = reconcile_scope(scope, rows)
    assert report.counts["clean"] == 1
    assert report.counts["duid_mismatch"] == 0
