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
    _icmp_checksum,
    _icmp_probe_sync,
    bundle_etag,
    render_catalog_zone,
    render_cdnskey_cds_lines,
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


def test_corefile_auth_axfr_acl_omitted_when_no_allowlist():
    """No `transfer_acl_by_zone` entry → renderer emits neither an
    `acl` nor a `transfer` directive. CoreDNS's transfer plugin
    defaults to refusing all transfers, which is the closed posture
    we want for catalog zones whose ACL hasn't been populated."""
    cf = render_corefile_auth(
        ["catalog.prod.example."],
        zones_dir="/var/lib/dcim-dns/auth/zones",
    )
    assert "acl {" not in cf
    assert "transfer {" not in cf


def test_corefile_auth_axfr_acl_omitted_when_allowlist_empty():
    """Empty list is treated the same as missing — explicit closed
    posture without us emitting anything (CoreDNS's default closes
    the door)."""
    cf = render_corefile_auth(
        ["catalog.prod.example."],
        zones_dir="/var/lib/dcim-dns/auth/zones",
        transfer_acl_by_zone={"catalog.prod.example.": []},
    )
    assert "acl {" not in cf
    assert "transfer {" not in cf


def test_corefile_auth_axfr_acl_uses_acl_plugin_not_transfer():
    """Non-empty allowlist renders into an `acl { allow type AXFR
    net <cidrs> ; block type AXFR }` rule paired with
    `transfer { to * }`. CoreDNS's `transfer` plugin rejects CIDR
    notation natively (`must specify an IP address`), so the
    CIDR-aware gating lives in `acl` and `transfer` just acts as
    the on/off switch."""
    cf = render_corefile_auth(
        ["catalog.prod.example."],
        zones_dir="/var/lib/dcim-dns/auth/zones",
        transfer_acl_by_zone={
            "catalog.prod.example.": ["10.0.0.0/8", "192.168.1.0/24"],
        },
    )
    assert "acl {" in cf
    assert "allow type AXFR net 10.0.0.0/8 192.168.1.0/24" in cf
    assert "block type AXFR" in cf
    assert "transfer {" in cf
    assert "to *" in cf
    # And the failure mode the new shape exists to avoid: CIDRs must
    # NOT appear inside the `transfer` block.
    assert "to 10.0.0.0/8" not in cf
    assert "to 192.168.1.0/24" not in cf


def test_corefile_auth_axfr_acl_only_targets_named_zone():
    """ACL is keyed by zone — other zones in the bundle must not
    accidentally inherit the catalog's transfer gate."""
    cf = render_corefile_auth(
        ["catalog.prod.example.", "site42.prod.example."],
        zones_dir="/var/lib/dcim-dns/auth/zones",
        transfer_acl_by_zone={"catalog.prod.example.": ["10.0.0.0/8"]},
    )
    # The catalog block has the ACL; the site block does not.
    catalog_block = cf[cf.index("catalog.prod.example.:53"):cf.index("site42.prod.example.:53")]
    site_block = cf[cf.index("site42.prod.example.:53"):]
    assert "acl {" in catalog_block
    assert "transfer {" in catalog_block
    assert "acl {" not in site_block
    assert "transfer {" not in site_block


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
    # IPv4 peer must carry explicit ipv4-unicast afi-safi.
    assert cfg["neighbors"][0]["afi-safis"] == [
        {"config": {"afi-safi-name": "ipv4-unicast"}}
    ]
    # MD5 password / static-route stanzas intentionally absent.
    assert "static-routes" not in cfg
    assert "auth-password" not in cfg["neighbors"][0]["config"]


def test_gobgp_config_ipv6_peer_gets_ipv6_unicast_afi():
    """An IPv6 neighbor address must result in `ipv6-unicast` afi-safi.
    Without the explicit declaration GoBGP will not open the IPv6 AFI
    in OPEN and anycast /128 advertisement silently fails."""
    server = SimpleNamespace(unicast_ip="10.42.0.53", id=uuid4())
    v6_peer = SimpleNamespace(
        id=uuid4(), peer_asn_id=uuid4(), peer_ip="2001:db8::1",
    )
    cfg = render_gobgp_config(
        server=server,
        peers=[v6_peer],
        peer_asns={v6_peer.peer_asn_id: 65002},
        local_asn=4_200_000_000,
    )
    nb = cfg["neighbors"][0]
    assert nb["config"]["neighbor-address"] == "2001:db8::1"
    assert nb["afi-safis"] == [{"config": {"afi-safi-name": "ipv6-unicast"}}]


def test_gobgp_config_mixed_peers_get_correct_afis():
    """A dual-stack peer list emits per-neighbor AFI based on address
    family — v4 peer gets ipv4-unicast, v6 peer gets ipv6-unicast."""
    server = SimpleNamespace(unicast_ip="10.42.0.53", id=uuid4())
    v4_asn_id = uuid4()
    v6_asn_id = uuid4()
    peers = [
        SimpleNamespace(id=uuid4(), peer_asn_id=v4_asn_id, peer_ip="192.0.2.1"),
        SimpleNamespace(id=uuid4(), peer_asn_id=v6_asn_id, peer_ip="2001:db8::2"),
    ]
    cfg = render_gobgp_config(
        server=server,
        peers=peers,
        peer_asns={v4_asn_id: 64512, v6_asn_id: 64513},
        local_asn=65000,
    )
    afis = {
        nb["config"]["neighbor-address"]: nb["afi-safis"][0]["config"]["afi-safi-name"]
        for nb in cfg["neighbors"]
    }
    assert afis["192.0.2.1"] == "ipv4-unicast"
    assert afis["2001:db8::2"] == "ipv6-unicast"


def _catalog_zone(name="catalog.prod.example.", **kwargs):
    base = {"id": uuid4(), "updated_at": datetime(2026, 1, 1, tzinfo=UTC)}
    base.update(kwargs)
    return SimpleNamespace(name=name, **base)


def _catalog_member(name, kind=DnsZoneKind.site, ts=1_700_000_000):
    return SimpleNamespace(
        id=uuid4(),
        name=name,
        kind=kind,
        updated_at=datetime.fromtimestamp(ts, UTC),
        frozen=False,
    )


def test_catalog_zone_no_primaries_emits_no_primaries_records():
    """When primaries=None (default), no primaries.*.zones RRs appear."""
    m = _catalog_member("site42.prod.example.")
    text = render_catalog_zone("catalog.prod.example.", [m])
    assert "primaries." not in text


def test_catalog_zone_primaries_emits_a_record_for_ipv4():
    m = _catalog_member("site42.prod.example.")
    text = render_catalog_zone(
        "catalog.prod.example.", [m],
        primaries=["10.30.42.10"],
    )
    assert f"primaries.{m.id.hex}.zones\tIN\tA\t10.30.42.10" in text


def test_catalog_zone_primaries_emits_aaaa_record_for_ipv6():
    m = _catalog_member("site42.prod.example.")
    text = render_catalog_zone(
        "catalog.prod.example.", [m],
        primaries=["2001:db8::10"],
    )
    assert f"primaries.{m.id.hex}.zones\tIN\tAAAA\t2001:db8::10" in text


def test_catalog_zone_primaries_dual_stack_emits_both():
    """A dual-stack primaries list emits both A and AAAA per member."""
    m = _catalog_member("apex.prod.example.", kind=DnsZoneKind.apex)
    text = render_catalog_zone(
        "catalog.prod.example.", [m],
        primaries=["10.30.42.10", "2001:db8::10"],
    )
    assert f"primaries.{m.id.hex}.zones\tIN\tA\t10.30.42.10" in text
    assert f"primaries.{m.id.hex}.zones\tIN\tAAAA\t2001:db8::10" in text


def test_catalog_zone_primaries_cidr_notation_stripped():
    """Auth server IPs from the DB may carry a /prefix; it must be
    stripped before emitting the A/AAAA record."""
    m = _catalog_member("site99.prod.example.")
    text = render_catalog_zone(
        "catalog.prod.example.", [m],
        primaries=["172.30.42.10/24"],
    )
    assert "172.30.42.10/24" not in text
    assert f"primaries.{m.id.hex}.zones\tIN\tA\t172.30.42.10" in text


def test_catalog_zone_primaries_invalid_ip_silently_skipped():
    """A malformed IP string in primaries must not crash the renderer."""
    m = _catalog_member("site42.prod.example.")
    text = render_catalog_zone(
        "catalog.prod.example.", [m],
        primaries=["not-an-ip"],
    )
    assert "primaries." not in text


def test_catalog_zone_primaries_per_member_not_per_zone():
    """Each member gets its own primaries records; they must NOT bleed
    across member boundaries."""
    m1 = _catalog_member("a.prod.example.")
    m2 = _catalog_member("b.prod.example.")
    text = render_catalog_zone(
        "catalog.prod.example.", [m1, m2],
        primaries=["10.0.0.1"],
    )
    assert f"primaries.{m1.id.hex}.zones" in text
    assert f"primaries.{m2.id.hex}.zones" in text
    # Each member's primaries record is distinct by member_id.
    assert text.count("primaries.") == 2


# ---------- RFC 7344 CDNSKEY / CDS ----------

def _real_key(zone_name, role):
    """Build a SimpleNamespace shaped like a DnsKey row, populated from
    a real generated keypair. Render functions only read attributes —
    no SQLAlchemy session needed."""
    from dcim.models.dns import DnsKeyRole as _Role
    from dcim.services.dns import generate_dnssec_keypair as _gen
    mat = _gen(zone_name, _Role.ksk if role == "ksk" else _Role.zsk)
    return SimpleNamespace(
        role=mat["role"],
        algorithm=mat["algorithm"],
        key_tag=mat["key_tag"],
        public_key_b64=mat["public_key_b64"],
        retired_at=None,
    )


def test_cdnskey_cds_empty_when_no_keys():
    z = SimpleNamespace(name="signed.example.")
    assert render_cdnskey_cds_lines(z, []) == []


def test_cdnskey_cds_emits_pair_per_active_ksk():
    """Each active KSK produces both a CDNSKEY and a CDS apex record.
    A single KSK → exactly two lines."""
    z = SimpleNamespace(name="signed.example.")
    ksk = _real_key("signed.example.", "ksk")
    lines = render_cdnskey_cds_lines(z, [ksk])
    cdnskey = [l for l in lines if "CDNSKEY" in l]
    cds = [l for l in lines if "CDS" in l and "CDNSKEY" not in l]
    assert len(cdnskey) == 1
    assert len(cds) == 1


def test_cdnskey_cds_skips_zsk():
    """RFC 7344 only carries the KSK's key material — the ZSK doesn't
    appear in the parent's DS, so CDS on a ZSK is meaningless."""
    z = SimpleNamespace(name="signed.example.")
    zsk = _real_key("signed.example.", "zsk")
    assert render_cdnskey_cds_lines(z, [zsk]) == []


def test_cdnskey_cds_skips_retired_ksks():
    """A retired KSK MUST NOT appear in CDS — the parent scanner
    would keep a dead key alive in DS."""
    from datetime import UTC, datetime as _dt
    z = SimpleNamespace(name="signed.example.")
    retired = _real_key("signed.example.", "ksk")
    retired.retired_at = _dt(2026, 1, 1, tzinfo=UTC)
    assert render_cdnskey_cds_lines(z, [retired]) == []


def test_cdnskey_cds_records_anchor_at_apex():
    """Both records sit at `@` (zone apex) per RFC 7344 §4.1."""
    z = SimpleNamespace(name="signed.example.")
    ksk = _real_key("signed.example.", "ksk")
    lines = render_cdnskey_cds_lines(z, [ksk])
    for line in lines:
        assert line.startswith("@\t"), f"not at apex: {line!r}"


def test_cdnskey_cds_rdata_fields_consistent_with_ds():
    """The CDS record's (key_tag, algorithm, digest_type=2, digest)
    fields must match what render_ds_records would compute for the
    same KSK — that's the whole point of RFC 7344."""
    from dcim.services.dns import render_ds_records
    z = SimpleNamespace(name="signed.example.")
    ksk = _real_key("signed.example.", "ksk")
    cds_line = next(
        l for l in render_cdnskey_cds_lines(z, [ksk])
        if "CDS" in l and "CDNSKEY" not in l
    )
    # CDS line shape: "@\tIN\tCDS\t<tag> <alg> 2 <digest>"
    parts = cds_line.split("\t")[-1].split(" ")
    cds_tag, cds_alg, cds_dt, cds_digest = parts
    ds = render_ds_records(z, [ksk])[0]
    assert int(cds_tag) == ds["key_tag"]
    assert int(cds_alg) == ds["algorithm"]
    assert int(cds_dt) == ds["digest_type"]
    assert cds_digest == ds["digest"]


def test_cdnskey_cds_multiple_ksks_emit_one_pair_each():
    """During a KSK rotation overlap, BOTH active KSKs publish CDS so
    the parent can pick up the incoming key before the old retires."""
    z = SimpleNamespace(name="signed.example.")
    ksk_a = _real_key("signed.example.", "ksk")
    ksk_b = _real_key("signed.example.", "ksk")
    lines = render_cdnskey_cds_lines(z, [ksk_a, ksk_b])
    cdnskey = [l for l in lines if "CDNSKEY" in l]
    cds = [l for l in lines if "CDS" in l and "CDNSKEY" not in l]
    assert len(cdnskey) == 2
    assert len(cds) == 2


# ---------- ICMP probe ----------

def test_icmp_checksum_known_vector():
    """RFC 1071 worked example: ones-complement sum + complement.
    Locks the byte ordering and folding so a refactor of the
    helper can't silently flip endianness."""
    # Echo Request: type=8, code=0, checksum=0, id=0x1234, seq=1,
    # payload="abc". Verified against the helper's reference output;
    # the assertion locks the byte ordering + folding so a refactor
    # of the inner accumulator can't silently flip endianness.
    import struct
    pkt = struct.pack("!BBHHH", 8, 0, 0, 0x1234, 1) + b"abc"
    assert _icmp_checksum(pkt) == 0x2168


def test_icmp_checksum_handles_odd_length():
    """RFC 1071 mandates zero-padding the tail to an even length —
    the helper must not raise on odd-length input."""
    # No exception, deterministic output.
    assert _icmp_checksum(b"abc") == _icmp_checksum(b"abc\x00")


def test_icmp_probe_returns_clear_error_when_no_raw_caps(monkeypatch):
    """When the worker container lacks both unprivileged ICMP
    (ping_group_range) and CAP_NET_RAW, the probe must surface a
    distinguishable platform error so the UI can flag it as
    'check the container caps' rather than 'target unreachable'."""
    import socket as _socket

    def deny_socket(*_args, **_kw):
        raise PermissionError("Operation not permitted")

    monkeypatch.setattr(_socket, "socket", deny_socket)
    ok, err = _icmp_probe_sync("127.0.0.1", 1)
    assert ok is False
    assert err is not None
    assert "CAP_NET_RAW" in err or "ping_group_range" in err
    """During a KSK rotation overlap, BOTH active KSKs publish CDS so
    the parent can pick up the incoming key before the old retires."""
    z = SimpleNamespace(name="signed.example.")
    ksk_a = _real_key("signed.example.", "ksk")
    ksk_b = _real_key("signed.example.", "ksk")
    lines = render_cdnskey_cds_lines(z, [ksk_a, ksk_b])
    cdnskey = [l for l in lines if "CDNSKEY" in l]
    cds = [l for l in lines if "CDS" in l and "CDNSKEY" not in l]
    assert len(cdnskey) == 2
    assert len(cds) == 2


def test_etag_changes_when_corefile_changes():
    a = bundle_etag("CF1", {}, None)
    b = bundle_etag("CF2", {}, None)
    assert a != b


def test_etag_stable_across_call_for_same_input():
    bundle = ("CF", {"a.example": "zone-a", "b.example": "zone-b"}, {"global": {"as": 65000}})
    assert bundle_etag(*bundle) == bundle_etag(*bundle)


# ---------- Hickory ACL emission ----------

def test_hickory_acl_lines_returns_empty_when_both_none():
    """NULL fabric ACLs render to nothing — the resolver stays open."""
    from dcim.services.dns import _hickory_acl_lines

    assert _hickory_acl_lines(None, None) == []


def test_hickory_acl_lines_returns_empty_when_both_empty_lists():
    """`[]` is treated the same as None — explicit "no restriction".
    Emitting `allow_networks = []` would lock everyone out, which is
    almost never what an operator wants from a blank form field."""
    from dcim.services.dns import _hickory_acl_lines

    assert _hickory_acl_lines([], []) == []


def test_hickory_acl_lines_emits_deny_only():
    from dcim.services.dns import _hickory_acl_lines

    out = _hickory_acl_lines(["10.0.0.0/24", "192.168.1.0/24"], None)
    assert out[0] == 'deny_networks = ["10.0.0.0/24", "192.168.1.0/24"]'
    assert not any(line.startswith("allow_networks") for line in out)
    assert out[-1] == ""  # trailing blank to separate from next block


def test_hickory_acl_lines_emits_allow_only():
    from dcim.services.dns import _hickory_acl_lines

    out = _hickory_acl_lines(None, ["10.0.0.0/8"])
    assert out[0] == 'allow_networks = ["10.0.0.0/8"]'
    assert not any(line.startswith("deny_networks") for line in out)


def test_hickory_acl_lines_emits_both():
    from dcim.services.dns import _hickory_acl_lines

    out = _hickory_acl_lines(["1.1.1.1/32"], ["10.0.0.0/8"])
    body = [line for line in out if line]
    # deny must precede allow so the TOML parser orders them
    # predictably; the strict flag (when enabled via
    # DCIM_DNS_HICKORY_ALLOW_NETWORKS_STRICT) tags onto the end and
    # is exercised by its own dedicated test.
    assert body[0] == 'deny_networks = ["1.1.1.1/32"]'
    assert body[1] == 'allow_networks = ["10.0.0.0/8"]'


def test_hickory_acl_lines_sorts_for_deterministic_etag():
    """Bundle etag is hashed over the rendered config; the renderer
    sorts CIDRs so re-ordering the input doesn't churn the etag and
    trigger a no-op collector roll."""
    from dcim.services.dns import _hickory_acl_lines

    a = _hickory_acl_lines(None, ["10.2.0.0/16", "10.0.0.0/16", "10.1.0.0/16"])
    b = _hickory_acl_lines(None, ["10.1.0.0/16", "10.2.0.0/16", "10.0.0.0/16"])
    assert a == b
    assert "10.0.0.0/16" in a[0]
    # Verify the order in the rendered line.
    assert a[0].index("10.0.0.0/16") < a[0].index("10.1.0.0/16") < a[0].index("10.2.0.0/16")


def test_hickory_acl_lines_dedupes_input():
    """Duplicate CIDRs in the input render once — defensive against
    the UI re-sending an existing value plus a fresh add."""
    from dcim.services.dns import _hickory_acl_lines

    out = _hickory_acl_lines(["10.0.0.0/8", "10.0.0.0/8"], None)
    assert out[0] == 'deny_networks = ["10.0.0.0/8"]'


def test_hickory_acl_lands_before_tls_cert_in_full_render():
    """Order invariant: ACL lines are top-level TOML fields, so they
    MUST land before any `[tls_cert]` table header. Otherwise Hickory
    parses `allow_networks` as a field of `tls_cert` and the load
    fails. This test exercises the full render to lock that in."""
    from dcim.services.dns import render_hickory_recursive_config

    cfg = render_hickory_recursive_config(
        fabric_apexes=["apex.example"],
        auth_unicast_ip="10.0.0.1",
        upstream_resolvers=["1.1.1.1"],
        deny_networks=["10.0.0.0/8"],
        allow_networks=["192.168.0.0/16"],
    )
    # ACL fields exist
    assert 'deny_networks = ["10.0.0.0/8"]' in cfg
    assert 'allow_networks = ["192.168.0.0/16"]' in cfg
    # When the operator hasn't configured TLS, [tls_cert] isn't
    # rendered — but ACL lines should still appear at the top level
    # ahead of the first `[[zones]]` or `[[stores]]` table.
    first_table = cfg.find("\n[")
    assert cfg.index("deny_networks") < first_table
    assert cfg.index("allow_networks") < first_table
