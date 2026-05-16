"""Unit tests for the Kea client's pure parsing + matching layer.

`KeaClient._post` and the SQL-touching `sync_*` orchestrators are I/O
bound and live in the integration suite. These tests pin the lease
parser, the cltt + valid-lft expiry math, and the longest-prefix-match
subnet picker so future Kea protocol changes can't silently regress.
"""

from datetime import UTC, datetime
from types import SimpleNamespace
from uuid import uuid4

from dcim.services.kea import (
    _extract_leases,
    lease_valid_until,
    match_lease_to_subnet,
    parse_kea_lease,
)


def _subnet(prefix: str):
    return SimpleNamespace(id=uuid4(), prefix=prefix)


# ---------- lease_valid_until ----------

def test_lease_valid_until_nominal():
    cltt = 1746792000  # 2025-05-09 12:00:00 UTC
    out = lease_valid_until(cltt, 3600)
    assert out is not None
    assert abs(
        (out - datetime(2025, 5, 9, 13, 0, tzinfo=UTC)).total_seconds()
    ) < 1


def test_lease_valid_until_missing_pieces_is_none():
    assert lease_valid_until(None, 3600) is None
    assert lease_valid_until(123, None) is None
    assert lease_valid_until(None, None) is None


def test_lease_valid_until_handles_string_inputs_kea_sometimes_sends():
    """Kea has been observed to stringify cltt + valid-lft on certain
    versions — the parser should still cope."""
    out = lease_valid_until("1746792000", "3600")  # type: ignore[arg-type]
    assert out is not None


def test_lease_valid_until_garbage_returns_none():
    assert lease_valid_until("not-a-number", 60) is None  # type: ignore[arg-type]


# ---------- parse_kea_lease ----------

def test_parse_kea_lease_basic_v4():
    raw = {
        "ip-address": "10.0.0.42",
        "hw-address": "00:11:22:33:44:55",
        "hostname": "host-42",
        "cltt": 1746792000,
        "valid-lft": 3600,
        "state": 0,
    }
    parsed = parse_kea_lease(raw)
    assert parsed is not None
    assert parsed.address == "10.0.0.42"
    assert parsed.mac == "00:11:22:33:44:55"
    assert parsed.hostname == "host-42"
    assert parsed.valid_until is not None
    assert parsed.state == 0


def test_parse_kea_lease_skips_declined():
    parsed = parse_kea_lease({"ip-address": "10.0.0.5", "state": 1})
    assert parsed is None


def test_parse_kea_lease_skips_expired_reclaimed():
    parsed = parse_kea_lease({"ip-address": "10.0.0.5", "state": 2})
    assert parsed is None


def test_parse_kea_lease_skips_when_no_address():
    parsed = parse_kea_lease({"hw-address": "aa:bb:cc:dd:ee:ff"})
    assert parsed is None


def test_parse_kea_lease_v6_uses_duid_when_hw_missing():
    raw = {
        "ip-address": "2001:db8::42",
        "duid": "00:01:00:01:abcd",
        "hostname": "v6-host",
        "state": 0,
    }
    parsed = parse_kea_lease(raw)
    assert parsed is not None
    assert parsed.mac == "00:01:00:01:abcd"
    assert parsed.address == "2001:db8::42"


def test_parse_kea_lease_treats_empty_hostname_as_none():
    raw = {"ip-address": "10.0.0.5", "hw-address": "x", "hostname": "", "state": 0}
    parsed = parse_kea_lease(raw)
    assert parsed is not None
    assert parsed.hostname is None


def test_parse_kea_lease_missing_state_defaults_to_active():
    raw = {"ip-address": "10.0.0.5", "hw-address": "x"}
    parsed = parse_kea_lease(raw)
    assert parsed is not None
    assert parsed.state == 0


# ---------- match_lease_to_subnet ----------

def test_match_lease_picks_longest_prefix():
    """A lease at 10.0.5.7 should land in the /24, not the /16, when both exist."""
    subnets = [_subnet("10.0.0.0/16"), _subnet("10.0.5.0/24"), _subnet("10.0.5.0/28")]
    out = match_lease_to_subnet("10.0.5.7", subnets)
    assert out is not None
    # /28 is the longest prefix containing .5.7? .7 is in 10.0.5.0/28 (covers .0-.15)
    assert out.prefix == "10.0.5.0/28"


def test_match_lease_returns_none_when_no_subnet_covers():
    subnets = [_subnet("10.0.0.0/16")]
    assert match_lease_to_subnet("192.168.1.5", subnets) is None


def test_match_lease_handles_empty_subnet_list():
    assert match_lease_to_subnet("10.0.0.5", []) is None


def test_match_lease_picks_v6_subnet_for_v6_address():
    subnets = [_subnet("10.0.0.0/24"), _subnet("2001:db8::/32")]
    out = match_lease_to_subnet("2001:db8:1::1", subnets)
    assert out is not None
    assert out.prefix == "2001:db8::/32"


def test_match_lease_skips_subnets_with_unparseable_prefix():
    subnets = [_subnet("not-a-cidr"), _subnet("10.0.0.0/24")]
    out = match_lease_to_subnet("10.0.0.5", subnets)
    assert out is not None
    assert out.prefix == "10.0.0.0/24"


# ---------- _extract_leases ----------

def test_extract_leases_pulls_arguments_leases_array():
    resp = [
        {"result": 0, "arguments": {"leases": [{"ip-address": "10.0.0.1"}]}},
        {"result": 0, "arguments": {"leases": [{"ip-address": "10.0.0.2"}]}},
    ]
    out = _extract_leases(resp)
    assert len(out) == 2
    assert {row["ip-address"] for row in out} == {"10.0.0.1", "10.0.0.2"}


def test_extract_leases_ignores_error_results():
    resp = [
        {"result": 1, "text": "boom"},
        {"result": 0, "arguments": {"leases": [{"ip-address": "10.0.0.1"}]}},
    ]
    out = _extract_leases(resp)
    assert len(out) == 1


def test_extract_leases_treats_empty_result_3_as_ok():
    """Kea returns result=3 ("empty") for services with no leases; that's
    a successful response, not an error."""
    resp = [{"result": 3, "text": "no leases"}]
    out = _extract_leases(resp)
    assert out == []


def test_extract_leases_handles_non_list_top_level():
    assert _extract_leases({"result": 0}) == []
    assert _extract_leases(None) == []


def test_extract_leases_handles_missing_arguments_gracefully():
    resp = [{"result": 0}]
    assert _extract_leases(resp) == []
