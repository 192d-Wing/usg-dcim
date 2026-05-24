"""IPMI driver — sensor data record (SDR) walk via python-ipmi (optional dep)."""

from __future__ import annotations

from datetime import UTC, datetime

import structlog

from ..config import DeviceConfig

log = structlog.get_logger("collector.ipmi")


class IpmiDriver:
    name = "ipmi"

    async def poll(self, device: DeviceConfig) -> list[dict]:
        cfg = device.ipmi
        if cfg is None:
            return []
        try:
            import pyipmi  # type: ignore
            import pyipmi.interfaces  # type: ignore
        except Exception:  # pragma: no cover
            log.error("python-ipmi not installed; skipping device", host=cfg.host)
            return []

        # python-ipmi is sync; wrap in a thread executor in production.
        ts = datetime.now(UTC).isoformat()
        out: list[dict] = []
        try:
            interface = pyipmi.interfaces.create_interface("ipmitool", interface_type="lanplus")
            ipmi = pyipmi.create_connection(interface)
            ipmi.target = pyipmi.Target(0x20)
            ipmi.session.set_session_type_rmcp(cfg.host, port=623)
            ipmi.session.set_auth_type_user(cfg.username, cfg.password_ref or "")
            ipmi.session.establish()
            for sensor in ipmi.sdr_repository_entries():
                try:
                    rdr = ipmi.get_sensor_reading(sensor.number)
                    if rdr.value is None:
                        continue
                    out.append({
                        "asset_id": device.asset_id,
                        "metric": f"ipmi.{sensor.device_id_string.strip().lower().replace(' ', '_')}",
                        "value": float(rdr.value),
                        "unit": None,
                        "ts": ts,
                        "tags": {"sensor_number": sensor.number},
                    })
                except Exception:
                    continue
        except Exception as e:  # pragma: no cover
            log.warning("ipmi_poll_failed", err=str(e))
        return out
