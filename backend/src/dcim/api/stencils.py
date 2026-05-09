"""Vendor stencil catalog.

Stencils describe how to render a piece of hardware in the rack visualization:
appearance metadata (front color, accent, vendor logo) and optional `image_url`
pointing to a vendor-provided SVG. The frontend renders an "image" stencil if
`image_url` is set, otherwise it falls back to a procedural SVG built from
`accent`, `kind_hint`, and `port_count`.

In a future iteration this lives in Postgres so admins can curate per-org. For
now it's a static, additive catalog so the frontend doesn't have to ship the
list in its bundle. Match precedence: exact (manufacturer, model) > manufacturer
default > kind default.
"""

from __future__ import annotations

from fastapi import APIRouter, Depends

from ..security.capabilities import INVENTORY_READ
from ..security.deps import Principal, require_capability

router = APIRouter(prefix="/stencils", tags=["stencils"])


# Vendor brand palette. Drives the front color band on procedural stencils.
VENDOR_PALETTE: dict[str, dict[str, str]] = {
    "dell":     {"primary": "#0085c3", "accent": "#003a70"},
    "hpe":      {"primary": "#01a982", "accent": "#003c2c"},
    "hp":       {"primary": "#01a982", "accent": "#003c2c"},
    "cisco":    {"primary": "#1ba0d7", "accent": "#005073"},
    "juniper":  {"primary": "#84bd00", "accent": "#003359"},
    "arista":   {"primary": "#f47920", "accent": "#1f2937"},
    "lenovo":   {"primary": "#e2231a", "accent": "#1f2937"},
    "supermicro": {"primary": "#c8102e", "accent": "#1a1a1a"},
    "ibm":      {"primary": "#0530ad", "accent": "#001a4f"},
    "apc":      {"primary": "#3dae2b", "accent": "#1a1a1a"},
    "schneider": {"primary": "#3dae2b", "accent": "#0a3a2a"},
    "eaton":    {"primary": "#005eb8", "accent": "#1a1a1a"},
    "vertiv":   {"primary": "#00a3e0", "accent": "#0033a0"},
    "liebert":  {"primary": "#00a3e0", "accent": "#0033a0"},
    "tripp lite": {"primary": "#0066b3", "accent": "#1a1a1a"},
    "raritan":  {"primary": "#005bbb", "accent": "#1a1a1a"},
    "panduit":  {"primary": "#0070c0", "accent": "#1a1a1a"},
    "demo":     {"primary": "#6b7280", "accent": "#374151"},
}


# Curated catalog. (manufacturer, model) -> stencil definition.
# `kind_hint` overrides the asset's kind for rendering purposes (e.g. blade chassis vs blade).
# `image_url` (optional) points to a vendor SVG/PNG served from /static/stencils/...
CATALOG: list[dict] = [
    # Dell
    {"manufacturer": "Dell", "model": "PowerEdge R650",  "u": 1, "kind_hint": "server",  "port_count": 4},
    {"manufacturer": "Dell", "model": "PowerEdge R750",  "u": 2, "kind_hint": "server",  "port_count": 4},
    {"manufacturer": "Dell", "model": "PowerEdge R760",  "u": 2, "kind_hint": "server",  "port_count": 4},
    {"manufacturer": "Dell", "model": "PowerEdge R940",  "u": 3, "kind_hint": "server",  "port_count": 4},
    {"manufacturer": "Dell", "model": "Networking S5248", "u": 1, "kind_hint": "switch", "port_count": 48},
    # HPE
    {"manufacturer": "HPE",  "model": "ProLiant DL360 Gen11", "u": 1, "kind_hint": "server", "port_count": 4},
    {"manufacturer": "HPE",  "model": "ProLiant DL380 Gen11", "u": 2, "kind_hint": "server", "port_count": 4},
    {"manufacturer": "HPE",  "model": "ProLiant DL580 Gen11", "u": 4, "kind_hint": "server", "port_count": 4},
    {"manufacturer": "HPE",  "model": "Synergy 12000",        "u": 10, "kind_hint": "chassis"},
    # Cisco
    {"manufacturer": "Cisco", "model": "Nexus 93180YC-FX",   "u": 1, "kind_hint": "switch", "port_count": 48},
    {"manufacturer": "Cisco", "model": "Nexus 9336C-FX2",    "u": 1, "kind_hint": "switch", "port_count": 36},
    {"manufacturer": "Cisco", "model": "Catalyst 9300-48P",  "u": 1, "kind_hint": "switch", "port_count": 48},
    {"manufacturer": "Cisco", "model": "ASR 1001-X",         "u": 1, "kind_hint": "router", "port_count": 6},
    {"manufacturer": "Cisco", "model": "UCS C240 M6",        "u": 2, "kind_hint": "server", "port_count": 4},
    # Arista
    {"manufacturer": "Arista", "model": "7050SX3-48YC8",  "u": 1, "kind_hint": "switch", "port_count": 48},
    # Juniper
    {"manufacturer": "Juniper", "model": "QFX5120-48Y",   "u": 1, "kind_hint": "switch", "port_count": 48},
    # APC PDUs
    {"manufacturer": "APC", "model": "AP8941",
     "u": 0, "kind_hint": "pdu", "port_count": 24, "vertical": True},
    {"manufacturer": "APC", "model": "AP8853",
     "u": 0, "kind_hint": "pdu", "port_count": 21, "vertical": True},
    {"manufacturer": "APC", "model": "AP7900",  "u": 1, "kind_hint": "pdu", "port_count": 8},
    # APC UPS
    {"manufacturer": "APC", "model": "Smart-UPS SRT 5000VA", "u": 3, "kind_hint": "ups"},
    {"manufacturer": "APC", "model": "Symmetra LX 16kVA",    "u": 12, "kind_hint": "ups"},
    # Eaton UPS
    {"manufacturer": "Eaton", "model": "9PX 6000",     "u": 3, "kind_hint": "ups"},
    {"manufacturer": "Eaton", "model": "BladeUPS 12kW", "u": 6, "kind_hint": "ups"},
    # CRACs / Cooling
    {"manufacturer": "Liebert", "model": "CRV CR020",  "u": 0, "kind_hint": "crac"},
    {"manufacturer": "Vertiv",  "model": "Liebert DSE", "u": 0, "kind_hint": "crac"},
    # Sensors
    {"manufacturer": "Raritan", "model": "DPX2-T1H1",  "u": 0, "kind_hint": "sensor"},
    # Demo (matches the seeded enterprise)
    {"manufacturer": "Demo", "model": "X1", "u": 1, "kind_hint": "server", "port_count": 4},
]


@router.get("")
async def list_stencils(
    _: Principal = Depends(require_capability(INVENTORY_READ)),
) -> dict:
    """Return the vendor stencil catalog and palette for the frontend renderer."""
    return {"palette": VENDOR_PALETTE, "stencils": CATALOG}
