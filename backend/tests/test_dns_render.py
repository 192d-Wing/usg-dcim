"""Unit tests for the pure DNS render functions.

Bundle assembly + zone rendering happen often (every collector poll),
and the contract with CoreDNS is brittle — a comma off in BIND output
breaks the file plugin silently. Lock the format here so refactors
can't drift.
"""

from datetime import UTC, datetime
from types import SimpleNamespace
from uuid import uuid4

from dcim.models.dns import DnsRecordSource, DnsRecordType, DnsZoneKind
from dcim.services.dns import (
    bundle_etag,
    render_corefile_auth,
    render_corefile_recursive,
    render_gobgp_config,
    render_zone_file,
)


def _zone(**overrides):
    """Build a zone-shaped object without touching SQLAlchemy. The
    renderer reads attributes; any object will do."""
    base = {
        "id": uuid4(),
        "name": "site42.prod.dcim.mil",
        "kind": DnsZoneKind.site,
        "soa_mname": "ns1",
        "soa_rname": "hostmaster",
        "soa_refresh": 3600,
        "soa_retry": 600,
        "soa_expire": 604800,
        "soa_minimum": 300,
        "default_ttl": 300,
        "updated_at": datetime(2026, 5, 10, tzinfo=UTC),
    }
    base.update(overrides)
    return SimpleNamespace(**base)


def _record(name, rtype, data, ttl=None, source=DnsRecordSource.manual):
    return SimpleNamespace(
        id=uuid4(), name=name, type=rtype, ttl=ttl, data=data, source=source,
    )


def test_zone_file_has_origin_ttl_soa():
    zone = _zone()
    text = render_zone_file(zone, [])
    assert "$ORIGIN site42.prod.dcim.mil." in text
    assert "$TTL 300" in text
    assert "IN\tSOA\tns1.site42.prod.dcim.mil." in text


def test_zone_file_orders_records_for_diffability():
    zone = _zone()
    records = [
        _record("web", DnsRecordType.A, {"target": "10.0.0.20"}),
        _record("db", DnsRecordType.A, {"target": "10.0.0.10"}),
        _record("web", DnsRecordType.AAAA, {"target": "2001:db8::20"}),
    ]
    out = render_zone_file(zone, records)
    db_idx = out.index("db\t")
    web_a = out.index("web\t300\tIN\tA")
    web_aaaa = out.index("web\t300\tIN\tAAAA")
    assert db_idx < web_a < web_aaaa


def test_zone_file_emits_mx_priority_target():
    zone = _zone()
    rec = _record("@", DnsRecordType.MX, {"priority": 10, "target": "mail.example.com."})
    text = render_zone_file(zone, [rec])
    assert "@\t300\tIN\tMX\t10 mail.example.com." in text


def test_zone_file_quotes_txt():
    zone = _zone()
    rec = _record("@", DnsRecordType.TXT, {"text": 'v=spf1 include:_spf.x ~all'})
    text = render_zone_file(zone, [rec])
    assert '"v=spf1 include:_spf.x ~all"' in text


def test_zone_file_srv_format():
    zone = _zone()
    rec = _record(
        "_sip._tcp", DnsRecordType.SRV,
        {"priority": 10, "weight": 60, "port": 5060, "target": "sip.example.com."},
    )
    text = render_zone_file(zone, [rec])
    assert "_sip._tcp\t300\tIN\tSRV\t10 60 5060 sip.example.com." in text


def test_corefile_auth_contains_zone_blocks():
    cf = render_corefile_auth(["a.example.", "b.example."])
    assert "a.example.:53 {" in cf
    assert "b.example.:53 {" in cf
    assert "file /etc/coredns/zones/a.example..zone" in cf


def test_corefile_recursive_includes_apex_stub_when_set():
    cf = render_corefile_recursive(
        fabric_apex="prod.dcim.mil",
        auth_unicast_ip="10.42.0.53",
        upstream_resolvers=["1.1.1.1"],
    )
    assert "prod.dcim.mil:53 {" in cf
    assert "forward . 10.42.0.53:53" in cf
    assert ".:53 {" in cf
    assert "forward . 1.1.1.1" in cf


def test_corefile_recursive_falls_back_to_default_upstreams():
    cf = render_corefile_recursive(
        fabric_apex=None, auth_unicast_ip=None, upstream_resolvers=[],
    )
    # When the operator hasn't configured upstreams, the renderer picks a
    # public resolver default rather than emitting an empty `forward .`.
    assert "1.1.1.1" in cf or "8.8.8.8" in cf
    assert "prod.dcim.mil:53" not in cf


def test_gobgp_config_has_neighbor_and_anycast_network():
    server = SimpleNamespace(unicast_ip="10.42.0.53", id=uuid4())
    peer = SimpleNamespace(local_asn=65000, peer_asn=65001, peer_ip="10.42.255.1", md5_password=None)
    anycast = SimpleNamespace(
        anycast_ipv4="10.255.0.53", anycast_ipv6="2001:db8::53",
    )
    cfg = render_gobgp_config(server=server, peers=[peer], anycast_group=anycast)
    assert cfg["global"]["config"]["as"] == 65000
    assert cfg["neighbors"][0]["config"]["neighbor-address"] == "10.42.255.1"
    prefixes = {n["config"]["prefix"] for n in cfg["static-routes"]}
    assert "10.255.0.53/32" in prefixes
    assert "2001:db8::53/128" in prefixes


def test_gobgp_md5_password_passes_through():
    server = SimpleNamespace(unicast_ip="10.42.0.53", id=uuid4())
    peer = SimpleNamespace(local_asn=65000, peer_asn=65001, peer_ip="10.42.255.1", md5_password="secret")
    anycast = SimpleNamespace(anycast_ipv4="10.255.0.53", anycast_ipv6=None)
    cfg = render_gobgp_config(server=server, peers=[peer], anycast_group=anycast)
    assert cfg["neighbors"][0]["config"]["auth-password"] == "secret"


def test_etag_changes_when_corefile_changes():
    a = bundle_etag("CF1", {}, None)
    b = bundle_etag("CF2", {}, None)
    assert a != b


def test_etag_stable_across_call_for_same_input():
    bundle = ("CF", {"a.example": "zone-a", "b.example": "zone-b"}, {"global": {"as": 65000}})
    assert bundle_etag(*bundle) == bundle_etag(*bundle)
