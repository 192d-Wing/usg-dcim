"""Assemble a complete Kea config bundle for a DhcpServer (PR 76).

Symmetric with services/dns.render_bundle_for_server: the dhcp-site
chart's bundle-puller GETs `/api/v1/dhcp/servers/{id}/bundle`, writes
the three returned config blobs to disk, and signals Kea to reload.

DCIM owns the per-subnet arrays (`subnet4[]` / `subnet6[]`, rendered
from DhcpScope rows). The operator owns everything else Kea needs —
`interfaces-config`, `lease-database`, `hooks-libraries`, `loggers`,
global option-data, client-classes — and stores it in
DhcpServer.base_config as:

    {"ctrl-agent": {...}, "dhcp4": {...}, "dhcp6": {...}}

The renderer overlays the DCIM subnet arrays onto the operator's
dhcp4/dhcp6 sections — anything else in those sections passes
through verbatim. If the operator's section already carries a
`subnet4`/`subnet6` array, the DCIM-rendered one wins (DCIM is the
source of truth for subnet objects on a managed server).

Etag is the SHA256 of the canonical JSON serialization of the three
sections together. The dhcp-site puller short-circuits on etag
match; full bundle bytes still travel on every request, which is
cheap relative to a Kea reload but cheaper than streaming partial
content.
"""

from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass
from typing import Iterable

from ..models.ipam import DhcpScope, DhcpServer
from .dhcp_push import render_kea_subnet4, render_kea_subnet6


@dataclass
class KeaBundle:
    server_id: str
    ctrl_agent: dict
    dhcp4: dict
    dhcp6: dict
    etag: str


def _next_id(used: set[int]) -> int:
    """Lowest free positive int. Same allocator as push_scope uses,
    but here it runs against the in-memory rendered set — every
    scope gets an id, including ones never pushed yet, so the bundle
    is internally consistent without mutating DB state."""
    candidate = 1
    while candidate in used:
        candidate += 1
    return candidate


def _assemble_subnets(scopes: Iterable[DhcpScope]) -> tuple[list[dict], list[dict]]:
    """Render every enabled scope into Kea subnet4/subnet6 objects.

    Disabled scopes are skipped entirely — they shouldn't appear in
    the running Kea config. Each scope's kea_subnet_id is reused if
    set; unpushed scopes get a fresh allocation inside this bundle
    pass that doesn't touch the DB (the DB-side allocation belongs
    to push_scope; this is bundle-internal only).
    """
    subnet4: list[dict] = []
    subnet6: list[dict] = []
    used4: set[int] = set()
    used6: set[int] = set()
    deferred4: list[DhcpScope] = []
    deferred6: list[DhcpScope] = []

    # First pass — claim every pinned id so allocations don't collide.
    for s in scopes:
        if not s.enabled:
            continue
        if s.kea_subnet_id is None:
            (deferred4 if s.ip_family == 4 else deferred6).append(s)
            continue
        if s.ip_family == 4:
            used4.add(int(s.kea_subnet_id))
            subnet4.append(render_kea_subnet4(s, s.kea_subnet_id))
        else:
            used6.add(int(s.kea_subnet_id))
            subnet6.append(render_kea_subnet6(s, s.kea_subnet_id))

    # Second pass — fill in unpushed scopes with bundle-local ids.
    for s in deferred4:
        kid = _next_id(used4)
        used4.add(kid)
        subnet4.append(render_kea_subnet4(s, kid))
    for s in deferred6:
        kid = _next_id(used6)
        used6.add(kid)
        subnet6.append(render_kea_subnet6(s, kid))

    return subnet4, subnet6


def _overlay_subnets(base: dict, key: str, subnets: list[dict]) -> dict:
    """Replace `base[key]` with the DCIM-rendered subnet array, leaving
    every other key in `base` alone. Returns a new dict; the input is
    not mutated."""
    out = dict(base)
    out[key] = subnets
    return out


def _compute_etag(ctrl_agent: dict, dhcp4: dict, dhcp6: dict) -> str:
    """SHA256 of the canonical JSON. sort_keys=True so dict order
    doesn't perturb the digest; separators tight so whitespace
    drift in operator-authored base configs doesn't either."""
    canonical = json.dumps(
        {"ctrl-agent": ctrl_agent, "dhcp4": dhcp4, "dhcp6": dhcp6},
        sort_keys=True, separators=(",", ":"),
    )
    return hashlib.sha256(canonical.encode("utf-8")).hexdigest()


def render_kea_bundle(server: DhcpServer, scopes: Iterable[DhcpScope]) -> KeaBundle:
    """Build the bundle for one server.

    Pure: no DB calls. Caller passes the DhcpServer row and the list
    of DhcpScope rows that belong to it. Disabled scopes are skipped
    (do not appear in Kea); disabled servers still render normally —
    the bundle endpoint can refuse on its own if needed.
    """
    base = dict(server.base_config or {})
    base_ctrl = dict(base.get("ctrl-agent") or {})
    base_dhcp4 = dict(base.get("dhcp4") or {})
    base_dhcp6 = dict(base.get("dhcp6") or {})

    subnet4, subnet6 = _assemble_subnets(scopes)

    ctrl_agent = base_ctrl
    dhcp4 = _overlay_subnets(base_dhcp4, "subnet4", subnet4)
    dhcp6 = _overlay_subnets(base_dhcp6, "subnet6", subnet6)

    return KeaBundle(
        server_id=str(server.id),
        ctrl_agent=ctrl_agent,
        dhcp4=dhcp4,
        dhcp6=dhcp6,
        etag=_compute_etag(ctrl_agent, dhcp4, dhcp6),
    )
