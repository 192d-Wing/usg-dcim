"""Modbus TCP driver — reads holding/input registers per device config."""

from __future__ import annotations

from datetime import UTC, datetime

import structlog

from ..config import DeviceConfig

log = structlog.get_logger("collector.modbus")


class ModbusDriver:
    name = "modbus"

    async def poll(self, device: DeviceConfig) -> list[dict]:
        cfg = device.modbus
        if cfg is None:
            return []
        try:
            from pymodbus.client import AsyncModbusTcpClient
        except Exception as e:  # pragma: no cover
            log.error("pymodbus_missing", err=str(e))
            return []

        out: list[dict] = []
        ts = datetime.now(UTC).isoformat()
        client = AsyncModbusTcpClient(cfg.host, port=cfg.port)
        await client.connect()
        try:
            for metric, reg in cfg.registers.items():
                try:
                    if reg.type == "holding":
                        rr = await client.read_holding_registers(reg.address, count=1, slave=cfg.unit_id)
                    elif reg.type == "input_register":
                        rr = await client.read_input_registers(reg.address, count=1, slave=cfg.unit_id)
                    else:
                        continue
                    if rr.isError():
                        log.warning("modbus_error", metric=metric, addr=reg.address)
                        continue
                    raw = rr.registers[0]
                    out.append({
                        "asset_id": device.asset_id,
                        "metric": metric,
                        "value": raw * reg.scale,
                        "unit": None,
                        "ts": ts,
                        "tags": {"address": reg.address, "host": cfg.host},
                    })
                except Exception as e:  # pragma: no cover
                    log.warning("modbus_poll_failed", metric=metric, err=str(e))
        finally:
            client.close()
        return out
