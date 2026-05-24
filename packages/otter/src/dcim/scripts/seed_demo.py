"""Seed a small enterprise dataset for local dev.

3 regions x 2 sites x racks x assets, plus the built-in roles and one
admin user so the operator can sign in immediately after `docker compose up`.
"""

from __future__ import annotations

import asyncio
from uuid import uuid4

import bcrypt
from sqlalchemy import select

from ..db import async_session
from ..models.auth import Role, User, UserRole
from ..models.collectors import Collector, CollectorStatus
from ..models.inventory import (
    Asset,
    AssetKind,
    Building,
    LifecycleState,
    Rack,
    Region,
    Room,
    Row,
    Site,
)
from ..security.capabilities import BUILT_IN_ROLES


async def seed() -> None:
    async with async_session() as db:
        # roles
        for name, perms in BUILT_IN_ROLES.items():
            existing = (await db.execute(select(Role).where(Role.name == name))).scalar_one_or_none()
            if existing is None:
                db.add(Role(name=name, permission_codes=perms, is_system=True))
        await db.commit()

        # admin user
        admin = (await db.execute(select(User).where(User.email == "admin@dcim.local"))).scalar_one_or_none()
        if admin is None:
            admin = User(
                email="admin@dcim.local",
                display_name="Local Admin",
                password_hash=bcrypt.hashpw(b"changeme", bcrypt.gensalt()).decode(),
            )
            db.add(admin)
            await db.flush()
            ent_role = (await db.execute(select(Role).where(Role.name == "EnterpriseAdmin"))).scalar_one()
            db.add(UserRole(user_id=admin.id, role_id=ent_role.id))
            await db.commit()

        # regions + sites
        regions = []
        for code, name in [("CONUS", "Continental US"), ("EUCOM", "Europe"), ("INDOPACOM", "Indo-Pacific")]:
            r = (await db.execute(select(Region).where(Region.code == code))).scalar_one_or_none()
            if r is None:
                r = Region(code=code, name=name)
                db.add(r)
                await db.flush()
            regions.append(r)
        await db.commit()

        for region in regions:
            for i in range(2):
                code = f"{region.code}-{i+1:03d}"
                site = (await db.execute(select(Site).where(Site.code == code))).scalar_one_or_none()
                if site is None:
                    site = Site(
                        region_id=region.id,
                        name=f"{region.name} Site {i+1}",
                        code=code,
                        majcom="USAF" if region.code == "CONUS" else "USEUCOM",
                        organization="J6",
                        enclave="NIPR",
                        lifecycle_state=LifecycleState.active,
                    )
                    db.add(site)
                    await db.flush()
                    bld = Building(site_id=site.id, name="Main", code="B1")
                    db.add(bld)
                    await db.flush()
                    rm = Room(building_id=bld.id, name="DC1", code="DC1", design_kw=500.0)
                    db.add(rm)
                    await db.flush()
                    row = Row(room_id=rm.id, name="A", code="A")
                    db.add(row)
                    await db.flush()
                    # Mix of rack heights so the UI shows real variety: 24U cabinet,
                    # 42U standard, 45U taller cabinet, 48U full-height.
                    heights = [24, 42, 45, 48]
                    for ridx in range(4):
                        u = heights[ridx]
                        rack = Rack(
                            site_id=site.id, row_id=row.id, name=f"R{ridx+1:02d}", code=f"R{ridx+1:02d}",
                            u_height=u, max_kw=12.0,
                        )
                        db.add(rack)
                        await db.flush()
                        # Two vertical 0U PDUs (rear corners), one A-side, one B-side.
                        # APC AP8941 stencil = 24-outlet vertical.
                        from ..models.inventory import AssetFace, AssetMount, PduSide
                        from ..models.power import Outlet, PowerConnection
                        pdu_a = Asset(
                            site_id=site.id, rack_id=rack.id,
                            name=f"{rack.code}-PDU-A",
                            hostname=f"{code.lower()}-{rack.code.lower()}-pdu-a",
                            kind=AssetKind.pdu, manufacturer="APC", model="AP8941",
                            serial=str(uuid4())[:8],
                            face=AssetFace.rear, mount=AssetMount.vertical_left,
                            pdu_side=PduSide.a,
                        )
                        pdu_b = Asset(
                            site_id=site.id, rack_id=rack.id,
                            name=f"{rack.code}-PDU-B",
                            hostname=f"{code.lower()}-{rack.code.lower()}-pdu-b",
                            kind=AssetKind.pdu, manufacturer="APC", model="AP8941",
                            serial=str(uuid4())[:8],
                            face=AssetFace.rear, mount=AssetMount.vertical_right,
                            pdu_side=PduSide.b,
                        )
                        db.add_all([pdu_a, pdu_b])
                        await db.flush()
                        # 24 outlets per PDU; phase A on odd/even doesn't matter here.
                        outlets_a, outlets_b = [], []
                        for i in range(1, 25):
                            oa = Outlet(pdu_asset_id=pdu_a.id, position=i, label=f"{i:02d}",
                                        phase=PduSide.a, max_amps=10, receptacle="C13")
                            ob = Outlet(pdu_asset_id=pdu_b.id, position=i, label=f"{i:02d}",
                                        phase=PduSide.b, max_amps=10, receptacle="C13")
                            outlets_a.append(oa)
                            outlets_b.append(ob)
                            db.add_all([oa, ob])
                        await db.flush()

                        # Place rack-mount devices in the U-grid. Front face servers, rear sensor.
                        n_servers = max(2, min(8, u // 6))
                        slot = 1
                        servers_created: list[Asset] = []
                        for kind, n, kind_u, face in [
                            (AssetKind.server, n_servers, 2, AssetFace.front),
                            (AssetKind.sensor, 1, 1, AssetFace.rear),
                        ]:
                            for k in range(n):
                                if slot + kind_u - 1 > u:
                                    break
                                a = Asset(
                                    site_id=site.id, rack_id=rack.id,
                                    name=f"{rack.code}-{kind.value}{k+1}",
                                    hostname=f"{code.lower()}-{rack.code.lower()}-{kind.value}{k+1}",
                                    kind=kind, manufacturer="Demo", model="X1",
                                    serial=str(uuid4())[:8],
                                    rack_position_u=slot, rack_units=kind_u,
                                    face=face, mount=AssetMount.rack,
                                    psu_count=2 if kind == AssetKind.server else None,
                                )
                                db.add(a)
                                if kind == AssetKind.server:
                                    servers_created.append(a)
                                slot += kind_u
                        await db.flush()

                        # Connect each server's two PSUs to outlets on PDU-A + PDU-B (redundant by default).
                        # First server in odd-indexed racks gets only one PSU connected to demonstrate a
                        # "single-feed" gap that the redundancy badge will flag.
                        for idx, srv in enumerate(servers_created):
                            outlet_pos = idx + 1
                            if outlet_pos > 24:
                                break
                            db.add(PowerConnection(
                                outlet_id=outlets_a[outlet_pos - 1].id,
                                asset_id=srv.id, psu_index=1, cord_color="blue",
                            ))
                            # Skip second PSU for the first server in odd-numbered racks → "single" gap
                            if not (ridx % 2 == 0 and idx == 0):
                                db.add(PowerConnection(
                                    outlet_id=outlets_b[outlet_pos - 1].id,
                                    asset_id=srv.id, psu_index=2, cord_color="red",
                                ))
                    db.add(
                        Collector(
                            site_id=site.id, name=f"{code}-collector",
                            status=CollectorStatus.healthy, capabilities=["snmp", "redfish", "modbus"],
                        )
                    )
        await db.commit()
        print("Seed complete.")


if __name__ == "__main__":
    asyncio.run(seed())
