"""SNMP driver — polls OIDs declared per-device. Uses pysnmp-lextudio."""

from __future__ import annotations

from datetime import UTC, datetime

import structlog

from ..config import DeviceConfig

log = structlog.get_logger("collector.snmp")


class SnmpDriver:
    name = "snmp"

    async def poll(self, device: DeviceConfig) -> list[dict]:
        cfg = device.snmp
        if cfg is None:
            return []
        # Lazy-import so unit tests don't require pysnmp installed.
        try:
            from pysnmp.hlapi.v3arch.asyncio import (
                CommunityData,
                ContextData,
                ObjectIdentity,
                ObjectType,
                SnmpEngine,
                UdpTransportTarget,
                getCmd,
            )
        except Exception as e:  # pragma: no cover
            log.error("pysnmp_missing", err=str(e))
            return []

        engine = SnmpEngine()
        out: list[dict] = []
        ts = datetime.now(UTC).isoformat()
        for metric, oid in cfg.oids.items():
            errInd, errStat, _, varBinds = await getCmd(
                engine,
                CommunityData(cfg.community, mpModel=0 if cfg.version == "1" else 1),
                await UdpTransportTarget.create((cfg.host, cfg.port)),
                ContextData(),
                ObjectType(ObjectIdentity(oid)),
            )
            if errInd or errStat:
                log.warning("snmp_error", metric=metric, oid=oid, err=str(errInd or errStat))
                continue
            for _, val in varBinds:
                try:
                    value = float(val)
                except Exception:
                    continue
                out.append(
                    {
                        "asset_id": device.asset_id,
                        "metric": metric,
                        "value": value,
                        "unit": None,
                        "ts": ts,
                        "tags": {"oid": oid, "host": cfg.host},
                    }
                )
        return out
