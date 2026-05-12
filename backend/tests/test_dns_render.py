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
    cf = render_corefile_auth(
        ["a.example.", "b.example."],
        zones_dir="/var/lib/dcim-dns/auth/zones",
    )
    assert "a.example.:53 {" in cf
    assert "b.example.:53 {" in cf
    assert "file /var/lib/dcim-dns/auth/zones/a.example..zone" in cf


def test_corefile_auth_emits_dnssec_block_when_nsec3_unset():
    """Zone signed but no nsec3 params → upstream `dnssec` plugin
    (NSEC chains), which works against the stock coredns/coredns
    image. Keep this path pinned so a refactor of the signing-block
    helper can't silently break operators who haven't migrated to
    the custom image yet."""
    cf = render_corefile_auth(
        ["a.example."],
        zones_dir="/var/lib/dcim-dns/auth/zones",
        keys_dir="/var/lib/dcim-dns/auth/keys",
        dnssec_keys_by_zone={"a.example.": ["Ka.example.+013+12345"]},
    )
    assert "dnssec {" in cf
    assert "key file /var/lib/dcim-dns/auth/keys/Ka.example.+013+12345" in cf
    # No nsec3sign artifacts should leak into the NSEC path.
    assert "nsec3sign" not in cf
    assert "salt" not in cf


def test_corefile_auth_emits_nsec3sign_block_when_params_set():
    """Zone signed AND in nsec3_params_by_zone → our `nsec3sign`
    plugin (NSEC3 chains), which requires the custom coredns image.
    The plugin block carries salt + iterations + the zone-file path
    so the chain builder can walk the same zone at boot."""
    cf = render_corefile_auth(
        ["a.example."],
        zones_dir="/var/lib/dcim-dns/auth/zones",
        keys_dir="/var/lib/dcim-dns/auth/keys",
        dnssec_keys_by_zone={"a.example.": ["Ka.example.+013+12345"]},
        nsec3_params_by_zone={
            "a.example.": {"salt": "aabbccdd", "iterations": 0, "opt_out": False},
        },
    )
    assert "nsec3sign {" in cf
    assert "key file /var/lib/dcim-dns/auth/keys/Ka.example.+013+12345" in cf
    assert "zone file /var/lib/dcim-dns/auth/zones/a.example..zone" in cf
    assert 'salt "aabbccdd"' in cf
    assert "iterations 0" in cf
    # NSEC and NSEC3 are mutually exclusive per zone — the upstream
    # `dnssec` directive must NOT appear when nsec3sign does.
    assert "    dnssec {" not in cf
    # opt_out is False → the directive should NOT be emitted.
    assert "opt_out" not in cf


def test_corefile_auth_nsec3_opt_out_emits_directive():
    """opt_out=True surfaces the `opt_out` directive — operators
    on delegation-heavy zones expect this to elide insecure
    delegations from the NSEC3 chain."""
    cf = render_corefile_auth(
        ["a.example."],
        zones_dir="/var/lib/dcim-dns/auth/zones",
        keys_dir="/var/lib/dcim-dns/auth/keys",
        dnssec_keys_by_zone={"a.example.": ["Ka.example.+013+12345"]},
        nsec3_params_by_zone={
            "a.example.": {"salt": "", "iterations": 10, "opt_out": True},
        },
    )
    assert "opt_out" in cf
    assert 'salt ""' in cf
    assert "iterations 10" in cf


def test_corefile_auth_mixed_nsec_and_nsec3_zones():
    """A bundle with one NSEC3 zone and one NSEC zone must emit the
    right block per zone — operators migrating zone-by-zone need
    both paths to coexist in one Corefile."""
    cf = render_corefile_auth(
        ["nsec.example.", "nsec3.example."],
        zones_dir="/var/lib/dcim-dns/auth/zones",
        keys_dir="/var/lib/dcim-dns/auth/keys",
        dnssec_keys_by_zone={
            "nsec.example.":  ["Knsec.example.+013+11111"],
            "nsec3.example.": ["Knsec3.example.+013+22222"],
        },
        nsec3_params_by_zone={
            "nsec3.example.": {"salt": "", "iterations": 0, "opt_out": False},
        },
    )
    # NSEC zone gets the upstream `dnssec` block; NSEC3 zone gets
    # `nsec3sign`. Both appear exactly once.
    assert cf.count("    dnssec {") == 1
    assert cf.count("    nsec3sign {") == 1


def test_corefile_recursive_includes_apex_stubs_when_set():
    # Multiple apexes per fabric → one stub-forward block each, all
    # targeting the same local auth pod.
    cf = render_corefile_recursive(
        fabric_apexes=["prod.dcim.mil", "tenant.example"],
        auth_unicast_ip="10.42.0.53",
        upstream_resolvers=["1.1.1.1"],
    )
    assert "prod.dcim.mil:53 {" in cf
    assert "tenant.example:53 {" in cf
    assert cf.count("forward . 10.42.0.53:53") == 2
    assert ".:53 {" in cf
    assert "forward . 1.1.1.1" in cf


def test_corefile_recursive_falls_back_to_default_upstreams():
    cf = render_corefile_recursive(
        fabric_apexes=[], auth_unicast_ip=None, upstream_resolvers=[],
    )
    # When the operator hasn't configured upstreams, the renderer picks a
    # public resolver default rather than emitting an empty `forward .`.
    assert "1.1.1.1" in cf or "8.8.8.8" in cf
    assert "prod.dcim.mil:53" not in cf


def test_corefile_recursive_emits_conditional_forwarders():
    # Conditional forwarders create their own `<pattern>:53` blocks
    # routed to operator-declared upstreams, distinct from both the
    # apex stub-forward and the catch-all global upstreams.
    cf = render_corefile_recursive(
        fabric_apexes=["prod.dcim.mil"],
        auth_unicast_ip="10.42.0.53",
        upstream_resolvers=["1.1.1.1"],
        conditional_forwarders=[
            ("aws.internal.", ["10.250.0.2", "10.250.0.3"]),
            ("corp.example.", ["10.7.0.53:5353"]),
        ],
    )
    assert "aws.internal.:53 {" in cf
    assert "forward . 10.250.0.2 10.250.0.3" in cf
    assert "corp.example.:53 {" in cf
    assert "forward . 10.7.0.53:5353" in cf
    # Apex + catch-all still present.
    assert "prod.dcim.mil:53 {" in cf
    assert ".:53 {" in cf


def test_corefile_recursive_skips_empty_forwarder():
    # An entry with no upstreams shouldn't emit a half-formed forward
    # block — would be a Corefile parse error.
    cf = render_corefile_recursive(
        fabric_apexes=[], auth_unicast_ip=None,
        upstream_resolvers=["1.1.1.1"],
        conditional_forwarders=[("broken.example.", [])],
    )
    assert "broken.example.:53" not in cf


def test_corefile_recursive_emits_blocklist_templates():
    # Block + sinkhole live as `template` directives inside the
    # catch-all .:53 block, ahead of the forward.
    cf = render_corefile_recursive(
        fabric_apexes=[], auth_unicast_ip=None,
        upstream_resolvers=["1.1.1.1"],
        blocklists=[
            {
                "action": "block",
                "patterns": ["evil.example", "*.malware.example"],
                "sink_ipv4": None, "sink_ipv6": None,
            },
            {
                "action": "sinkhole",
                "patterns": ["ads.example"],
                "sink_ipv4": "10.0.0.250", "sink_ipv6": None,
            },
        ],
    )
    assert "template ANY ANY {" in cf
    assert "rcode NXDOMAIN" in cf
    # Wildcard patterns expand to `.+\.<body>\.?$`.
    assert r"^.+\.malware\.example\.?$" in cf
    # Sinkhole block (v4 only — v6 sink absent).
    assert "template IN A {" in cf
    assert "10 IN A 10.0.0.250" not in cf  # answer line is templated
    assert "10.0.0.250" in cf
    assert "template IN AAAA" not in cf


def test_gobgp_config_has_neighbor():
    # render_gobgp_config emits BGP global + neighbor stanzas; prefix
    # advertisement is now a runtime gRPC operation rather than a
    # config-file thing (gobgpd rejected the old `static-routes` /
    # `route-server` keys we used to emit, so they're gone).
    server = SimpleNamespace(unicast_ip="10.42.0.53", id=uuid4())
    peer_id = uuid4()
    peer = SimpleNamespace(id=peer_id, peer_asn_id=uuid4(),
                           peer_ip="10.42.255.1")
    cfg = render_gobgp_config(
        server=server,
        peers=[peer],
        peer_asns={peer.peer_asn_id: 65001},
        local_asn=4_200_000_000,
    )
    assert cfg["global"]["config"]["as"] == 4_200_000_000
    assert cfg["global"]["config"]["router-id"] == "10.42.0.53"
    assert cfg["neighbors"][0]["config"]["neighbor-address"] == "10.42.255.1"
    assert cfg["neighbors"][0]["config"]["peer-as"] == 65001
    # MD5 password / static-route stanzas intentionally absent.
    assert "static-routes" not in cfg
    assert "auth-password" not in cfg["neighbors"][0]["config"]


def test_etag_changes_when_corefile_changes():
    a = bundle_etag("CF1", {}, None)
    b = bundle_etag("CF2", {}, None)
    assert a != b


def test_etag_stable_across_call_for_same_input():
    bundle = ("CF", {"a.example": "zone-a", "b.example": "zone-b"}, {"global": {"as": 65000}})
    assert bundle_etag(*bundle) == bundle_etag(*bundle)
