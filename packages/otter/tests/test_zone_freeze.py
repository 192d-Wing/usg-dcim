"""Unit tests for the zone-freeze write lock.

`_assert_zone_unfrozen` is the single seam every mutating DNS route
calls before touching a zone or its records. The test exercises it
against a frozen + unfrozen zone stub so the routes' wiring boils
down to "did we call this helper" — covered indirectly by the
existing API integration smoke."""

from types import SimpleNamespace

import pytest

from dcim.api.dns import _assert_zone_unfrozen
from dcim.errors import ValidationError


def _zone(frozen: bool) -> SimpleNamespace:
    """Shaped like DnsZone with just the field the helper reads."""
    return SimpleNamespace(frozen=frozen, name="apex.example")


def test_unfrozen_zone_passes_through():
    """Happy path: no exception, no return value."""
    assert _assert_zone_unfrozen(_zone(frozen=False)) is None


def test_frozen_zone_raises_validation_error():
    with pytest.raises(ValidationError) as exc:
        _assert_zone_unfrozen(_zone(frozen=True))
    # 422 is the load-bearing piece — operators get a clean form-level
    # error rather than a 500 or a 403 (which would mean "you lack a
    # cap", which isn't the case here).
    assert exc.value.status_code == 422


def test_frozen_message_mentions_unfreeze():
    """The error string is what the UI surfaces to operators; make sure
    it tells them how to recover."""
    with pytest.raises(ValidationError) as exc:
        _assert_zone_unfrozen(_zone(frozen=True))
    msg = str(exc.value)
    assert "frozen" in msg.lower()
    assert "unfreeze" in msg.lower()
