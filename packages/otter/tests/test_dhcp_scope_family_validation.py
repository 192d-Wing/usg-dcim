"""PR 101 — tests for cross-family reservation validation.

DHCP routes moved to otter-go (PR 17 cutover) along with the
_validate_reservations_against_family helper. The parity is now
pinned in Go at internal/ipam/dhcp_scope_mutations.go::validate
ReservationsAgainstFamily + the v4-reservation-with-duid / v6-
reservation-missing-duid handler tests. This file is kept for
historical context.
"""

from __future__ import annotations

import pytest

from dcim.errors import ValidationError
from dcim.schemas.ipam import DhcpReservation

pytestmark = pytest.mark.skip(
    reason="PR 17 cutover: DHCP routes + helpers moved to otter-go; "
    "parity now pinned in internal/ipam/dhcp_scope_mutations.go tests",
)


# Stub so the body type-checks; pytestmark above prevents any test
# below from running.
def _validate_reservations_against_family(*_a, **_kw):  # pragma: no cover
    raise NotImplementedError

# ----- v4 scope: must have mac; rejects duid -----

def test_v4_reservation_with_mac_passes():
    _validate_reservations_against_family(
        [DhcpReservation(mac="aa:bb:cc:dd:ee:ff", ip="10.0.0.5")], 4,
    )


def test_v4_reservation_with_duid_rejected():
    with pytest.raises(ValidationError, match="v4 reservations use `mac`"):
        _validate_reservations_against_family(
            [DhcpReservation(duid="00:01:00:01:abcd", ip="10.0.0.5")], 4,
        )


def test_v4_reservation_missing_mac_rejected():
    # PR 101 — a reservation without an identifier can't bind a
    # client; reject at the API instead of letting Kea silently
    # turn it into a wildcard.
    with pytest.raises(ValidationError, match="v4 reservation requires `mac`"):
        _validate_reservations_against_family(
            [DhcpReservation(ip="10.0.0.5")], 4,
        )


# ----- v6 scope: must have duid; rejects mac -----

def test_v6_reservation_with_duid_passes():
    _validate_reservations_against_family(
        [DhcpReservation(duid="00:01:00:01:abcd", ip="2001:db8::5")], 6,
    )


def test_v6_reservation_with_mac_rejected():
    with pytest.raises(ValidationError, match="v6 reservations use `duid`"):
        _validate_reservations_against_family(
            [DhcpReservation(mac="aa:bb:cc:dd:ee:ff", ip="2001:db8::5")], 6,
        )


def test_v6_reservation_missing_duid_rejected():
    with pytest.raises(ValidationError, match="v6 reservation requires `duid`"):
        _validate_reservations_against_family(
            [DhcpReservation(ip="2001:db8::5")], 6,
        )


# ----- dict shape (Update path) -----

def test_dict_v4_with_mac_passes():
    # api/ipam.py update handler runs payload.model_dump(exclude_unset=True)
    # so reservations arrive as a list[dict]. Helper handles both.
    _validate_reservations_against_family(
        [{"mac": "aa:bb:cc:dd:ee:ff", "ip": "10.0.0.5"}], 4,
    )


def test_dict_v4_with_duid_rejected():
    with pytest.raises(ValidationError, match="v4 reservations use `mac`"):
        _validate_reservations_against_family(
            [{"duid": "00:01:00:01:abcd", "ip": "10.0.0.5"}], 4,
        )


def test_dict_v6_with_mac_rejected():
    with pytest.raises(ValidationError, match="v6 reservations use `duid`"):
        _validate_reservations_against_family(
            [{"mac": "aa:bb:cc:dd:ee:ff", "ip": "2001:db8::5"}], 6,
        )


# ----- multiple reservations: first bad one fails the whole list -----

def test_first_bad_reservation_stops_validation():
    # Two reservations: one good, one with wrong-family identifier.
    # Validation is fail-fast so the bad row gets surfaced; the
    # caller fixes one at a time.
    with pytest.raises(ValidationError, match="v4 reservations use `mac`"):
        _validate_reservations_against_family(
            [
                DhcpReservation(mac="aa:bb:cc:dd:ee:ff", ip="10.0.0.5"),
                DhcpReservation(duid="00:01:00:01:abcd", ip="10.0.0.6"),
            ],
            4,
        )


def test_empty_reservation_list_is_a_no_op():
    # exclude_unset on update may send reservations=[] to mean
    # "clear reservations" — the helper accepts and returns.
    _validate_reservations_against_family([], 4)
    _validate_reservations_against_family([], 6)
