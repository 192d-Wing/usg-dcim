"""Device drivers — each returns a list of (metric, value, unit, tags) tuples."""

from __future__ import annotations

from typing import Protocol

from ..config import DeviceConfig


class Driver(Protocol):
    name: str

    async def poll(self, device: DeviceConfig) -> list[dict]:
        """Return a list of sample dicts: {asset_id, metric, value, unit, ts, tags}."""
        ...


def load_driver(name: str) -> Driver:
    from . import ipmi, modbus, redfish, rest, snmp

    table: dict[str, Driver] = {
        "snmp": snmp.SnmpDriver(),
        "redfish": redfish.RedfishDriver(),
        "modbus": modbus.ModbusDriver(),
        "rest": rest.RestDriver(),
        "ipmi": ipmi.IpmiDriver(),
    }
    if name not in table:
        raise ValueError(f"unknown driver: {name}")
    return table[name]
