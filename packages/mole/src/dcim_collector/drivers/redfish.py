"""Redfish driver — pulls thermal + power readings from BMCs."""

from __future__ import annotations

from datetime import UTC, datetime

import httpx
import structlog

from ..config import DeviceConfig

log = structlog.get_logger("collector.redfish")


class RedfishDriver:
    name = "redfish"

    async def poll(self, device: DeviceConfig) -> list[dict]:
        cfg = device.redfish
        if cfg is None:
            return []
        out: list[dict] = []
        ts = datetime.now(UTC).isoformat()
        async with httpx.AsyncClient(verify=cfg.verify_tls, timeout=10) as client:
            try:
                # Naive systems[0]/Thermal + Power probe — production would walk Chassis/...
                auth = httpx.BasicAuth(cfg.username, cfg.password or "")
                thermal = await client.get(f"{cfg.base_url}/redfish/v1/Chassis/1/Thermal", auth=auth)
                power = await client.get(f"{cfg.base_url}/redfish/v1/Chassis/1/Power", auth=auth)
            except Exception as e:  # pragma: no cover
                log.warning("redfish_fetch_failed", err=str(e))
                return []

            if thermal.status_code == 200:
                data = thermal.json()
                for t in data.get("Temperatures", []):
                    if (v := t.get("ReadingCelsius")) is not None:
                        out.append({
                            "asset_id": device.asset_id,
                            "metric": f"thermal.{t.get('Name', 'sensor').lower().replace(' ', '_')}.tempC",
                            "value": float(v),
                            "unit": "C",
                            "ts": ts,
                            "tags": {"sensor": t.get("Name")},
                        })
            if power.status_code == 200:
                data = power.json()
                for p in data.get("PowerControl", []):
                    if (v := p.get("PowerConsumedWatts")) is not None:
                        out.append({
                            "asset_id": device.asset_id,
                            "metric": "power.consumed.W",
                            "value": float(v),
                            "unit": "W",
                            "ts": ts,
                            "tags": {},
                        })
        return out
