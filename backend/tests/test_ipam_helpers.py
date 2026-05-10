"""Unit tests for the pure CIDR helpers powering IPAM validation.

The DB-backed assertions (`assert_subnet_inside_supernet` etc.) are
covered by the integration suite; these tests exercise containment,
overlap, address-membership, and next-free against `ipaddress` directly
so future refactors can't silently regress the invariants.
"""

import pytest

from dcim.errors import ValidationError
from dcim.services.ipam import (
    address_in_network,
    assert_purpose_compatible,
    cidr_contains,
    cidrs_overlap,
    network_capacity,
    next_free_address,
)

# ---------- cidr_contains ----------

def test_cidr_contains_same_network_is_true():
    assert cidr_contains("10.0.0.0/8", "10.0.0.0/8") is True


def test_cidr_contains_proper_subset_true():
    assert cidr_contains("10.0.0.0/8", "10.5.0.0/16") is True
    assert cidr_contains("10.5.0.0/16", "10.5.42.0/24") is True


def test_cidr_contains_disjoint_false():
    assert cidr_contains("10.0.0.0/8", "192.168.0.0/16") is False


def test_cidr_contains_parent_smaller_than_child_false():
    assert cidr_contains("10.0.0.0/24", "10.0.0.0/16") is False


def test_cidr_contains_v4_v6_mismatch_is_false():
    assert cidr_contains("10.0.0.0/8", "2001:db8::/32") is False
    assert cidr_contains("2001:db8::/32", "10.0.0.0/8") is False


def test_cidr_contains_handles_ipv6():
    assert cidr_contains("2001:db8::/32", "2001:db8:1::/48") is True
    assert cidr_contains("2001:db8::/32", "2001:db9::/32") is False


# ---------- cidrs_overlap ----------

def test_cidrs_overlap_disjoint():
    assert cidrs_overlap("10.0.0.0/24", "10.0.1.0/24") is False


def test_cidrs_overlap_one_contains_other():
    assert cidrs_overlap("10.0.0.0/16", "10.0.5.0/24") is True
    assert cidrs_overlap("10.0.5.0/24", "10.0.0.0/16") is True


def test_cidrs_overlap_partial_intersection():
    # /23 spans .0 and .1 — overlaps with .1.0/24
    assert cidrs_overlap("10.0.0.0/23", "10.0.1.0/24") is True


def test_cidrs_overlap_v4_v6_mismatch_is_false():
    assert cidrs_overlap("10.0.0.0/8", "::/0") is False


# ---------- address_in_network ----------

def test_address_in_network_inside():
    assert address_in_network("10.0.0.5", "10.0.0.0/24") is True
    assert address_in_network("10.0.0.255", "10.0.0.0/24") is True


def test_address_in_network_outside():
    assert address_in_network("10.0.1.5", "10.0.0.0/24") is False


def test_address_in_network_handles_inet_with_prefix():
    # asyncpg may hand us "10.0.0.5/32" or "10.0.0.5" — both must work.
    assert address_in_network("10.0.0.5/32", "10.0.0.0/24") is True


def test_address_in_network_v4_v6_mismatch_false():
    assert address_in_network("10.0.0.5", "2001:db8::/32") is False
    assert address_in_network("2001:db8::1", "10.0.0.0/24") is False


# ---------- next_free_address ----------

def test_next_free_address_in_empty_subnet():
    # /24 → first host is .1 (skipping .0 network + .255 broadcast)
    assert next_free_address("10.0.0.0/24", []) == "10.0.0.1"


def test_next_free_address_skips_used():
    used = ["10.0.0.1", "10.0.0.2", "10.0.0.3"]
    assert next_free_address("10.0.0.0/24", used) == "10.0.0.4"


def test_next_free_address_returns_none_when_full():
    # /30 → 4 addrs, 2 hosts (.1 and .2). Both used → no room.
    assert next_free_address("10.0.0.0/30", ["10.0.0.1", "10.0.0.2"]) is None


def test_next_free_address_p2p_31_includes_all_addresses():
    # On /31 RFC 3021 says both addresses are usable.
    assert next_free_address("10.0.0.0/31", []) == "10.0.0.0"
    assert next_free_address("10.0.0.0/31", ["10.0.0.0"]) == "10.0.0.1"


def test_next_free_address_single_host_32():
    # /32 has exactly one address.
    assert next_free_address("10.0.0.42/32", []) == "10.0.0.42"
    assert next_free_address("10.0.0.42/32", ["10.0.0.42"]) is None


def test_next_free_address_handles_ipv6():
    # /126 is a /126 — 4 addrs total, but at /127 and shorter ipv6
    # gets all addresses; here /126 gives us 4 addresses and 2 hosts.
    out = next_free_address("2001:db8::/126", [])
    assert out is not None and out.startswith("2001:db8::")


def test_next_free_address_strips_used_prefix_lengths():
    # asyncpg may hand us back "10.0.0.1/32"; the helper must still match.
    used = ["10.0.0.1/32", "10.0.0.2"]
    assert next_free_address("10.0.0.0/24", used) == "10.0.0.3"


# ---------- network_capacity ----------

@pytest.mark.parametrize("prefix,expected", [
    ("10.0.0.0/24", 254),  # 256 - network - broadcast
    ("10.0.0.0/30", 2),
    ("10.0.0.0/31", 2),    # P2P: both usable
    ("10.0.0.42/32", 1),   # single host
    ("2001:db8::/126", 2),
    ("2001:db8::/127", 2),
    ("2001:db8::/128", 1),
])
def test_network_capacity(prefix, expected):
    assert network_capacity(prefix) == expected


# ---------- assert_purpose_compatible ----------

def test_purpose_unset_supernet_imposes_no_constraint():
    """A generic supernet with no purpose should let any subnet purpose through."""
    assert_purpose_compatible(supernet_purpose=None, subnet_purpose="data")
    assert_purpose_compatible(supernet_purpose=None, subnet_purpose="mgmt")
    assert_purpose_compatible(supernet_purpose=None, subnet_purpose=None)


def test_purpose_unset_subnet_under_purposed_supernet_is_allowed():
    """An unlabeled subnet under a data supernet is fine — the operator
    just hasn't tagged it yet, and lookups still find it via the
    supernet's purpose."""
    assert_purpose_compatible(supernet_purpose="data", subnet_purpose=None)


def test_purpose_match_passes():
    assert_purpose_compatible(supernet_purpose="data", subnet_purpose="data")


def test_purpose_mismatch_raises():
    with pytest.raises(ValidationError) as exc:
        assert_purpose_compatible(supernet_purpose="data", subnet_purpose="mgmt")
    assert "doesn't match" in str(exc.value)
    assert exc.value.details["supernet_purpose"] == "data"
    assert exc.value.details["subnet_purpose"] == "mgmt"


# ---------- network_capacity int64 cap ----------

def test_network_capacity_caps_at_int64_for_huge_v6():
    """A /48 has 2^80 hosts, way past Postgres BIGINT and orjson's int64
    encoder. We clamp to int64 max so utilization responses serialize.
    Operators care about utilization percentage, not the exact count
    for prefixes that wide."""
    cap_48 = network_capacity("2001:db8::/48")
    assert cap_48 == (1 << 63) - 1
    # /64 (the typical assigned IPv6 subnet) also clamps — 2^64-2 still
    # exceeds int64 max.
    assert network_capacity("2001:db8::/64") == (1 << 63) - 1
    # /65 is the widest prefix where 2^63-2 fits unclamped.
    assert network_capacity("2001:db8::/65") == (1 << 63) - 2
