"""SNMPv2c/v3 GET driver.

Production: pysnmp-lextudio's async API. We keep the driver lazy-imports so the
collector boots even without pysnmp installed in dev.
"""

from __future__ import annotations

from datetime import UTC, datetime

import structlog

from ..config import DeviceCfg
from .base import Driver, Sample

log = structlog.get_logger("dcim.collector.snmp")


class SnmpDriver(Driver):
    name = "snmp"

    async def poll(self, device: DeviceCfg) -> list[Sample]:
        cfg = device.snmp or {}
        oids: dict[str, str] = cfg.get("oids", {})
        if not oids:
            return []

        try:
            from pysnmp.hlapi.v3arch.asyncio import (  # type: ignore[import-not-found]
                CommunityData, ContextData, ObjectIdentity, ObjectType, SnmpEngine, UdpTransportTarget,
                get_cmd,
            )
        except ImportError:
            log.warning("pysnmp_unavailable", asset=device.asset_id)
            return []

        engine = SnmpEngine()
        community = CommunityData(cfg.get("community", "public"))
        target = await UdpTransportTarget.create((device.address, device.port or 161))
        ctx = ContextData()

        out: list[Sample] = []
        now = datetime.now(UTC)
        for metric, oid in oids.items():
            error_indication, error_status, _err_index, var_binds = await get_cmd(
                engine, community, target, ctx, ObjectType(ObjectIdentity(oid))
            )
            if error_indication or error_status:
                log.warning("snmp_get_failed", oid=oid, err=str(error_indication or error_status))
                continue
            for _, value in var_binds:
                try:
                    out.append(Sample(asset_id=device.asset_id, metric=metric, value=float(value), ts=now))
                except (ValueError, TypeError):
                    continue
        return out
