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


def classify_redundancy(kind, connections: list[dict]) -> tuple[list[str], str]:
    """Pure classifier — given a device kind + its power connections, decide
    sides_covered and the redundancy verdict. Extracted so tests don't need a DB.

    `kind` is compared by value-equality to AssetKind.pdu (str enum) so the
    function works whether callers pass the enum, the string "pdu", or a
    SimpleNamespace stand-in.
    """
    pdu_value = AssetKind.pdu.value if hasattr(AssetKind.pdu, "value") else AssetKind.pdu
    if kind == AssetKind.pdu or kind == pdu_value:
        return [], "n/a"
    sides = sorted({c["pdu_side"] for c in connections if c.get("pdu_side")})
    if not connections:
        return sides, "unpowered"
    if len(sides) >= 2:
        return sides, "redundant"
    return sides, "single"


async def _load_outlets_and_connections(
    db: AsyncSession, pdu_ids: list,
) -> tuple[list[Outlet], list[PowerConnection]]:
    if not pdu_ids:
        return [], []
    outlets = list(
        (
            await db.execute(select(Outlet).where(Outlet.pdu_asset_id.in_(pdu_ids)))
        ).scalars().all()
    )
    if not outlets:
        return [], []
    conns = list(
        (
            await db.execute(
                select(PowerConnection).where(
                    PowerConnection.outlet_id.in_([o.id for o in outlets])
                )
            )
        ).scalars().all()
    )
    return outlets, conns


def _connection_row(pdu: Asset, outlet: Outlet, conn: PowerConnection) -> dict:
    return {
        "pdu_id": str(pdu.id),
        "pdu_name": pdu.name,
        "pdu_side": pdu.pdu_side.value if pdu.pdu_side else None,
        "outlet_id": str(outlet.id),
        "outlet_position": outlet.position,
        "outlet_label": outlet.label,
        "psu_index": conn.psu_index,
    }


def _empty_entry() -> dict:
    return {"sides_covered": [], "connections": [], "redundancy": "n/a"}


def _pdu_summary_row(p: Asset, outlets_for_p: list[Outlet], used_outlet_ids: set) -> dict:
    return {
        "id": str(p.id),
        "name": p.name,
        "side": p.pdu_side.value if p.pdu_side else None,
        "mount": p.mount.value if hasattr(p.mount, "value") else p.mount,
        "face": p.face.value if hasattr(p.face, "value") else p.face,
        "total_outlets": len(outlets_for_p),
        "used_outlets": sum(1 for o in outlets_for_p if o.id in used_outlet_ids),
    }


async def compute_power_chain(db: AsyncSession, assets: list[Asset]) -> dict:
    if not assets:
        return {"per_asset": {}, "pdus": []}

    pdus = [a for a in assets if a.kind == AssetKind.pdu]
    outlets_rows, conns = await _load_outlets_and_connections(db, [p.id for p in pdus])
    outlets_by_id = {o.id: o for o in outlets_rows}
    outlets_by_pdu: dict[str, list[Outlet]] = {}
    for o in outlets_rows:
        outlets_by_pdu.setdefault(str(o.pdu_asset_id), []).append(o)
    pdu_by_id = {str(p.id): p for p in pdus}

    per_asset: dict[str, dict] = {str(a.id): _empty_entry() for a in assets}
    for c in conns:
        outlet = outlets_by_id.get(c.outlet_id)
        pdu = pdu_by_id.get(str(outlet.pdu_asset_id)) if outlet else None
        if outlet is None or pdu is None:
            continue
        entry = per_asset.setdefault(str(c.asset_id), _empty_entry())
        entry["connections"].append(_connection_row(pdu, outlet, c))

    for a in assets:
        entry = per_asset[str(a.id)]
        sides, verdict = classify_redundancy(a.kind, entry["connections"])
        entry["sides_covered"] = sides
        entry["redundancy"] = verdict

    used_outlet_ids = {c.outlet_id for c in conns}
    pdu_summary = [
        _pdu_summary_row(p, outlets_by_pdu.get(str(p.id), []), used_outlet_ids)
        for p in pdus
    ]
    return {"per_asset": per_asset, "pdus": pdu_summary}
