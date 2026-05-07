"""Power-chain rollup for a rack.

Returns:
  per_asset: { asset_id: {
      sides_covered: ["A","B"],          # which PDU sides this device is fed from
      connections: [
        { pdu_id, pdu_name, pdu_side, outlet_id, outlet_position, psu_index }
      ],
      redundancy: "redundant" | "single" | "unpowered" | "n/a"
    }
  }
  pdus: [ { id, name, side, total_outlets, used_outlets } ]

Redundancy rules (rules of thumb that match real DC operations):
  - PDU asset itself: n/a
  - Device with no power_count constraint and no connections: unpowered
  - Device fed from outlets on 2+ different PDU sides: redundant
  - Device fed from at least one outlet but all on the same side: single
"""

from __future__ import annotations

from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from ..models.inventory import Asset, AssetKind
from ..models.power import Outlet, PowerConnection


async def compute_power_chain(db: AsyncSession, assets: list[Asset]) -> dict:
    if not assets:
        return {"per_asset": {}, "pdus": []}

    pdus = [a for a in assets if a.kind == AssetKind.pdu]
    pdu_ids = [p.id for p in pdus]

    # All outlets in this rack's PDUs
    outlets_rows = (
        await db.execute(select(Outlet).where(Outlet.pdu_asset_id.in_(pdu_ids) if pdu_ids else False))
    ).scalars().all() if pdu_ids else []
    outlets_by_id = {o.id: o for o in outlets_rows}
    outlets_by_pdu: dict[str, list[Outlet]] = {}
    for o in outlets_rows:
        outlets_by_pdu.setdefault(str(o.pdu_asset_id), []).append(o)

    # Connections from those outlets
    conns = (
        await db.execute(
            select(PowerConnection).where(
                PowerConnection.outlet_id.in_([o.id for o in outlets_rows]) if outlets_rows else False
            )
        )
    ).scalars().all() if outlets_rows else []

    pdu_by_id = {str(p.id): p for p in pdus}
    per_asset: dict[str, dict] = {}

    for a in assets:
        per_asset[str(a.id)] = {"sides_covered": [], "connections": [], "redundancy": "n/a"}

    for c in conns:
        outlet = outlets_by_id.get(c.outlet_id)
        if outlet is None:
            continue
        pdu = pdu_by_id.get(str(outlet.pdu_asset_id))
        if pdu is None:
            continue
        entry = per_asset.setdefault(str(c.asset_id), {"sides_covered": [], "connections": [], "redundancy": "n/a"})
        entry["connections"].append({
            "pdu_id": str(pdu.id),
            "pdu_name": pdu.name,
            "pdu_side": pdu.pdu_side.value if pdu.pdu_side else None,
            "outlet_id": str(outlet.id),
            "outlet_position": outlet.position,
            "outlet_label": outlet.label,
            "psu_index": c.psu_index,
        })

    # Classify redundancy
    for a in assets:
        aid = str(a.id)
        entry = per_asset[aid]
        if a.kind == AssetKind.pdu:
            entry["redundancy"] = "n/a"
            continue
        sides = sorted({c["pdu_side"] for c in entry["connections"] if c["pdu_side"]})
        entry["sides_covered"] = sides
        if not entry["connections"]:
            entry["redundancy"] = "unpowered"
        elif len(sides) >= 2:
            entry["redundancy"] = "redundant"
        else:
            entry["redundancy"] = "single"

    pdu_summary = []
    for p in pdus:
        outlets_for_p = outlets_by_pdu.get(str(p.id), [])
        used = sum(1 for o in outlets_for_p if any(c.outlet_id == o.id for c in conns))
        pdu_summary.append({
            "id": str(p.id),
            "name": p.name,
            "side": p.pdu_side.value if p.pdu_side else None,
            "mount": p.mount.value if hasattr(p.mount, "value") else p.mount,
            "face": p.face.value if hasattr(p.face, "value") else p.face,
            "total_outlets": len(outlets_for_p),
            "used_outlets": used,
        })

    return {"per_asset": per_asset, "pdus": pdu_summary}
