"""DNS render + projection helpers.

Pure functions live up here so the unit suite can drive zone-file,
Corefile, and GoBGP rendering without spinning up Postgres. The async
helpers below them touch the DB to:

  - project IPAM IPAddress.dns_name rows into A/AAAA/PTR records,
  - assemble the bundle a single DnsServer needs (Corefile + zones +
    GoBGP), and
  - hash the bundle for an etag so the collector can short-circuit.

The renderer emits BIND-format zone files because CoreDNS's `file`
plugin reads BIND. Deterministic ordering is important — the collector
diffs the bundle against its last etag, and a stable order keeps the
diff meaningful.
"""

from __future__ import annotations

import hashlib
import ipaddress
import json
from collections.abc import Iterable
from datetime import UTC, datetime
from uuid import UUID

from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from ..models.dns import (
    AnycastBgpBinding,
    AnycastGroup,
    BgpPeer,
    DnsRecord,
    DnsRecordSource,
    DnsRecordType,
    DnsServer,
    DnsServerRole,
    DnsZone,
    DnsZoneKind,
)
from ..models.ipam import IPAddress, Subnet
from ..settings import get_settings
from .ipam import parse_address, parse_network


# ---------- Zone serial number ----------

def _zone_serial(zone: DnsZone) -> int:
    """SOA serial. We use the zone's `updated_at` as a Unix timestamp,
    which is monotonic per zone and fits in 32 bits until year 2106 —
    plenty for the lifespan of a DCIM deployment. Operators who want a
    YYYYMMDDnn-style serial can override later; the renderer doesn't
    care as long as it goes up."""
    ts = zone.updated_at if zone.updated_at else datetime.now(UTC)
    return int(ts.timestamp())


# ---------- BIND record line emitters ----------

def _ttl_field(record_ttl: int | None, zone_default: int) -> str:
    return str(record_ttl if record_ttl is not None else zone_default)


def _format_record_line(record: DnsRecord, zone: DnsZone) -> str:
    """Emit one BIND-format RR line. The leading name uses '@' for
    apex; everything else is the bare label (relative to the zone
    origin) so the same record file works regardless of the zone's
    parent."""
    name = record.name if record.name else "@"
    ttl = _ttl_field(record.ttl, zone.default_ttl)
    rtype = record.type.value if hasattr(record.type, "value") else record.type
    data = record.data or {}
    rdata = _format_rdata(rtype, data)
    return f"{name}\t{ttl}\tIN\t{rtype}\t{rdata}"


def _format_rdata(rtype: str, data: dict) -> str:
    """Type-specific RDATA formatting. Schemas validated the shape, so
    we can reach into `data` without defensive .get()s."""
    if rtype in ("A", "AAAA", "CNAME", "NS", "PTR"):
        return data["target"]
    if rtype == "MX":
        return f"{data['priority']} {data['target']}"
    if rtype == "TXT":
        # Single-string TXT; quote and escape inner quotes per RFC 1035.
        text = data["text"].replace("\\", "\\\\").replace('"', '\\"')
        return f'"{text}"'
    if rtype == "SRV":
        return f"{data['priority']} {data['weight']} {data['port']} {data['target']}"
    if rtype == "CAA":
        # CAA values are quoted per RFC 6844.
        value = data["value"].replace('"', '\\"')
        return f'{data["flags"]} {data["tag"]} "{value}"'
    raise ValueError(f"unknown record type {rtype}")


def render_zone_file(zone: DnsZone, records: Iterable[DnsRecord]) -> str:
    """Emit a BIND-format zone file. Records are sorted by (name, type)
    for diffability."""
    rec_list = sorted(
        records,
        key=lambda r: (
            r.name or "",
            r.type.value if hasattr(r.type, "value") else r.type,
        ),
    )
    lines = [
        f"$ORIGIN {zone.name}.",
        f"$TTL {zone.default_ttl}",
        f"@\tIN\tSOA\t{zone.soa_mname}.{zone.name}. "
        f"{zone.soa_rname}.{zone.name}. (",
        f"\t\t\t{_zone_serial(zone)}\t; serial",
        f"\t\t\t{zone.soa_refresh}\t; refresh",
        f"\t\t\t{zone.soa_retry}\t; retry",
        f"\t\t\t{zone.soa_expire}\t; expire",
        f"\t\t\t{zone.soa_minimum})\t; minimum",
        "",
    ]
    for r in rec_list:
        lines.append(_format_record_line(r, zone))
    lines.append("")
    return "\n".join(lines)


# ---------- Corefile rendering ----------

def render_corefile_auth(zone_names: Iterable[str]) -> str:
    """Authoritative Corefile: one `file` block per zone, plus health,
    prometheus, errors, log."""
    blocks = []
    for name in sorted(zone_names):
        blocks.append(
            f"{name}:53 {{\n"
            f"    file /etc/coredns/zones/{name}.zone\n"
            f"    log\n"
            f"    errors\n"
            f"    prometheus :9153\n"
            f"    health :8080\n"
            f"}}"
        )
    return "\n\n".join(blocks) + "\n"


def render_corefile_recursive(
    *,
    fabric_apex: str | None,
    auth_unicast_ip: str | None,
    upstream_resolvers: Iterable[str],
) -> str:
    """Recursive Corefile: forward `*.<fabric_apex>` to the local auth
    pod, forward everything else to operator upstreams, plus
    cache/log/errors/prometheus/health."""
    upstream_list = " ".join(upstream_resolvers) or "1.1.1.1 8.8.8.8"
    blocks = []
    if fabric_apex and auth_unicast_ip:
        # Stub-zone forward — keeps internal lookups off the public root.
        blocks.append(
            f"{fabric_apex}:53 {{\n"
            f"    forward . {auth_unicast_ip}:53\n"
            f"    log\n"
            f"    errors\n"
            f"}}"
        )
    blocks.append(
        ".:53 {\n"
        f"    forward . {upstream_list}\n"
        "    cache 300\n"
        "    log\n"
        "    errors\n"
        "    prometheus :9153\n"
        "    health :8080\n"
        "}"
    )
    return "\n\n".join(blocks) + "\n"


# ---------- GoBGP rendering ----------

def render_gobgp_config(
    *,
    server: DnsServer,
    peers: Iterable[BgpPeer],
    peer_asns: dict,
    anycast_group: AnycastGroup,
    local_asn: int,
) -> dict:
    """GoBGP YAML config (returned as a dict; collector serializes to
    YAML on disk).

    `local_asn` is the originating AS for every DNS anycast
    announcement — pulled from settings.dns_anycast_originate_asn so
    every recursive site advertises from the same origin (default
    4200000000). The BgpPeer.local_asn_id on individual peers is
    informational here; we deliberately don't read it so a typo in a
    catalog row can't desync sites.

    `peer_asns` maps BgpPeer.peer_asn_id → ASN integer, resolved by the
    caller against the ASN catalog. Missing entries (dangling FK) get
    0 — GoBGP will reject the config, surfacing the bad reference at
    render time instead of silently advertising into nowhere."""
    peer_list = list(peers)
    neighbors = [
        {
            "config": {
                "neighbor-address": str(p.peer_ip).split("/", 1)[0],
                "peer-as": peer_asns.get(p.peer_asn_id, 0),
            },
        }
        for p in peer_list
    ]
    networks: list[dict] = []
    if anycast_group.anycast_ipv4:
        ip4 = str(anycast_group.anycast_ipv4).split("/", 1)[0]
        networks.append({"config": {"prefix": f"{ip4}/32"}})
    if anycast_group.anycast_ipv6:
        ip6 = str(anycast_group.anycast_ipv6).split("/", 1)[0]
        networks.append({"config": {"prefix": f"{ip6}/128"}})
    return {
        "global": {
            "config": {
                "as": local_asn,
                "router-id": str(server.unicast_ip).split("/", 1)[0],
            },
        },
        "neighbors": neighbors,
        "defined-sets": {},
        "policy-definitions": [],
        "route-server": {},
        "static-routes": networks,
    }


# ---------- Bundle assembly ----------

def _filename_for_zone(zone_name: str) -> str:
    """Drop the trailing dot if the operator typed a fully-qualified
    name; CoreDNS doesn't care but disk filenames do."""
    return zone_name.rstrip(".")


def bundle_etag(corefile: str, zones: dict[str, str], gobgp: dict | None) -> str:
    """Stable hash over the bundle so the collector can skip no-op
    pulls. Sorted JSON keeps the etag deterministic across renders."""
    h = hashlib.sha256()
    h.update(corefile.encode("utf-8"))
    h.update(b"\x00")
    for k in sorted(zones):
        h.update(k.encode("utf-8"))
        h.update(b"\x00")
        h.update(zones[k].encode("utf-8"))
        h.update(b"\x00")
    if gobgp is not None:
        h.update(json.dumps(gobgp, sort_keys=True).encode("utf-8"))
    return h.hexdigest()[:32]


async def _zones_for_server(db: AsyncSession, server: DnsServer) -> list[DnsZone]:
    """An auth pod loads every zone in its fabric (apex + all sites)
    for resilience — internal lookups never have to leave the box.
    A recursive pod loads no zone files; it's a forwarder only."""
    if server.role != DnsServerRole.auth:
        return []
    rows = (
        await db.execute(
            select(DnsZone).where(DnsZone.fabric_id == server.fabric_id)
        )
    ).scalars().all()
    return list(rows)


async def _records_by_zone(
    db: AsyncSession, zones: Iterable[DnsZone],
) -> dict[UUID, list[DnsRecord]]:
    zone_ids = [z.id for z in zones]
    if not zone_ids:
        return {}
    rows = (
        await db.execute(select(DnsRecord).where(DnsRecord.zone_id.in_(zone_ids)))
    ).scalars().all()
    grouped: dict[UUID, list[DnsRecord]] = {z.id: [] for z in zones}
    for r in rows:
        grouped[r.zone_id].append(r)
    return grouped


async def _local_auth_unicast_ip(db: AsyncSession, server: DnsServer) -> str | None:
    """For a recursive pod, find the auth pod at the same site so we
    can stub-zone the fabric apex back to it."""
    auth = (
        await db.execute(
            select(DnsServer).where(
                DnsServer.site_id == server.site_id,
                DnsServer.role == DnsServerRole.auth,
            )
        )
    ).scalar_one_or_none()
    return str(auth.unicast_ip).split("/", 1)[0] if auth else None


async def _fabric_apex_name(db: AsyncSession, fabric_id: UUID) -> str | None:
    apex = (
        await db.execute(
            select(DnsZone).where(
                DnsZone.fabric_id == fabric_id, DnsZone.kind == DnsZoneKind.apex,
            )
        )
    ).scalar_one_or_none()
    return apex.name if apex else None


async def _bgp_for_server(
    db: AsyncSession, server: DnsServer,
) -> tuple[list[BgpPeer], dict, AnycastGroup | None]:
    """Resolve the BGP peers a recursive server advertises to + the
    ASN-id → integer map for those peers + the server's anycast group.
    Returns ([], {}, None) for auth servers."""
    if server.role != DnsServerRole.recursive or server.anycast_group_id is None:
        return [], {}, None
    peer_ids = (
        await db.execute(
            select(AnycastBgpBinding.bgp_peer_id).where(
                AnycastBgpBinding.dns_server_id == server.id,
            )
        )
    ).scalars().all()
    peers: list[BgpPeer] = []
    peer_asns: dict = {}
    if peer_ids:
        peers = list((
            await db.execute(select(BgpPeer).where(BgpPeer.id.in_(peer_ids)))
        ).scalars().all())
        # One query to pull every ASN catalog row a peer points at.
        # The render function only needs peer_asn_id (the downstream
        # router AS); local_asn_id is intentionally ignored — the DNS
        # anycast origin AS is a system constant from settings.
        asn_ids = {p.peer_asn_id for p in peers}
        if asn_ids:
            from ..models.bgp import Asn  # avoid top-level cycle
            rows = (
                await db.execute(select(Asn).where(Asn.id.in_(asn_ids)))
            ).scalars().all()
            peer_asns = {row.id: row.asn for row in rows}
    anycast = await db.get(AnycastGroup, server.anycast_group_id)
    return peers, peer_asns, anycast


async def render_bundle_for_server(db: AsyncSession, server: DnsServer) -> dict:
    """One call returns the complete bundle a single server needs:
    Corefile, zone files, optional GoBGP config, etag.

    Bundle assembly is per-role:
      auth      -> zones for the whole fabric + authoritative Corefile.
      recursive -> empty zones, recursive Corefile (with stub for the
                   fabric apex), GoBGP config + anycast advertisement.
    """
    if server.role == DnsServerRole.auth:
        zones = await _zones_for_server(db, server)
        records_by_zone = await _records_by_zone(db, zones)
        zone_files = {
            _filename_for_zone(z.name): render_zone_file(z, records_by_zone.get(z.id, []))
            for z in zones
        }
        corefile = render_corefile_auth(z.name for z in zones)
        gobgp: dict | None = None
    else:
        # Recursive: assemble forwarders + stub for the fabric apex.
        apex_name = await _fabric_apex_name(db, server.fabric_id)
        local_auth_ip = await _local_auth_unicast_ip(db, server)
        # Operator-configured upstreams aren't modeled per-fabric yet
        # (deferred to a Fabric.dns_upstreams field). Default to public
        # quad-eight / cloudflare for the v1 plumbing.
        upstreams = ["1.1.1.1", "8.8.8.8"]
        corefile = render_corefile_recursive(
            fabric_apex=apex_name,
            auth_unicast_ip=local_auth_ip,
            upstream_resolvers=upstreams,
        )
        zone_files = {}
        peers, peer_asns, anycast = await _bgp_for_server(db, server)
        gobgp = render_gobgp_config(
            server=server, peers=peers, peer_asns=peer_asns,
            anycast_group=anycast,
            local_asn=get_settings().dns_anycast_originate_asn,
        ) if anycast else None
    etag = bundle_etag(corefile, zone_files, gobgp)
    return {"corefile": corefile, "zones": zone_files, "gobgp": gobgp, "etag": etag}


# ---------- IPAM → DNS projection ----------

def _ptr_owner(addr: str) -> str:
    """Compute the .in-addr.arpa / .ip6.arpa name for a given INET
    address (with no prefix length). Used by sync-from-ipam to write
    PTR records into the matching reverse zone."""
    a = parse_address(addr)
    if isinstance(a, ipaddress.IPv4Address):
        return ".".join(reversed(str(a).split("."))) + ".in-addr.arpa"
    # IPv6: each nibble reversed, dot-joined.
    nibbles = a.exploded.replace(":", "")
    return ".".join(reversed(nibbles)) + ".ip6.arpa"


async def sync_ipam_records_for_zone(
    db: AsyncSession, zone: DnsZone,
) -> tuple[int, int]:
    """Rebuild `source=ipam` records for a zone. Returns
    (added, removed). Replaces, never merges — IPAM is the source of
    truth for these rows.

    Only site zones get IPAM-projected records in v1. Apex zones are
    operator-curated (NS-delegations etc.).
    """
    if zone.kind != DnsZoneKind.site or zone.site_id is None:
        return (0, 0)

    # Drop existing ipam-projected rows in this zone.
    existing = (
        await db.execute(
            select(DnsRecord).where(
                DnsRecord.zone_id == zone.id,
                DnsRecord.source == DnsRecordSource.ipam,
            )
        )
    ).scalars().all()
    removed = len(existing)
    for r in existing:
        await db.delete(r)
    await db.flush()

    # Walk every IPAddress in every Subnet at the zone's site that has
    # a dns_name set, and emit A/AAAA records.
    subnet_rows = (
        await db.execute(select(Subnet).where(Subnet.site_id == zone.site_id))
    ).scalars().all()
    if not subnet_rows:
        return (0, removed)
    subnet_ids = [s.id for s in subnet_rows]
    ip_rows = (
        await db.execute(
            select(IPAddress).where(
                IPAddress.subnet_id.in_(subnet_ids),
                IPAddress.dns_name.is_not(None),
            )
        )
    ).scalars().all()

    added = 0
    for ip in ip_rows:
        addr_str = str(ip.address).split("/", 1)[0]
        try:
            a = parse_address(addr_str)
        except ValueError:
            continue
        rtype = DnsRecordType.AAAA if isinstance(a, ipaddress.IPv6Address) else DnsRecordType.A
        # Strip the zone suffix from the dns_name if the operator wrote
        # an FQDN; CoreDNS expects the bare label relative to $ORIGIN.
        label = ip.dns_name
        zone_suffix = "." + zone.name
        if label.endswith(zone_suffix):
            label = label[: -len(zone_suffix)]
        elif label == zone.name:
            label = "@"
        db.add(DnsRecord(
            zone_id=zone.id, name=label, type=rtype,
            data={"target": addr_str},
            source=DnsRecordSource.ipam,
            ipam_address_id=ip.id,
        ))
        added += 1
    await db.flush()
    return (added, removed)
