"""Collector config models."""

from __future__ import annotations

from pathlib import Path
from typing import Any, Literal
from uuid import UUID

import yaml
from pydantic import BaseModel, Field


class MtlsConfig(BaseModel):
    enabled: bool = True
    client_cert: str | None = None
    client_key: str | None = None
    ca_bundle: str | None = None


class SnmpDriverConfig(BaseModel):
    host: str
    port: int = 161
    community: str = "public"
    version: Literal["1", "2c", "3"] = "2c"
    oids: dict[str, str] = Field(default_factory=dict)


class RedfishDriverConfig(BaseModel):
    base_url: str
    username: str
    password_ref: str | None = None
    password: str | None = None
    verify_tls: bool = True


class ModbusRegister(BaseModel):
    address: int
    type: Literal["holding", "input_register", "coil", "discrete"] = "holding"
    scale: float = 1.0


class ModbusDriverConfig(BaseModel):
    host: str
    port: int = 502
    unit_id: int = 1
    registers: dict[str, ModbusRegister] = Field(default_factory=dict)


class RestDriverConfig(BaseModel):
    base_url: str
    headers: dict[str, str] = Field(default_factory=dict)
    paths: dict[str, str] = Field(default_factory=dict)  # metric -> json-path
    verify_tls: bool = True


class IpmiDriverConfig(BaseModel):
    host: str
    username: str
    password_ref: str | None = None


class DeviceConfig(BaseModel):
    asset_id: UUID
    kind: str
    driver: Literal["snmp", "redfish", "modbus", "rest", "ipmi"]
    poll_interval_seconds: int = 60
    snmp: SnmpDriverConfig | None = None
    redfish: RedfishDriverConfig | None = None
    modbus: ModbusDriverConfig | None = None
    rest: RestDriverConfig | None = None
    ipmi: IpmiDriverConfig | None = None


class DnsServerConfig(BaseModel):
    """One CoreDNS deployment the collector renders configs for. The
    collector polls /api/v1/dns/servers/{id}/bundle and writes the
    Corefile + zone files (+ gobgp.yaml when role=recursive) into
    `output_dir`, then signals the matching processes to reload."""

    id: UUID
    role: Literal["auth", "recursive"]
    output_dir: str
    coredns_pidfile: str
    gobgp_pidfile: str | None = None  # only set when role=recursive
    # Prometheus scrape URL for this server's CoreDNS process. CoreDNS
    # binds the prometheus plugin on :9153 in both our auth and
    # recursive Corefiles; override if the operator changes it.
    metrics_url: str = "http://127.0.0.1:9153/metrics"


class DnsAgentConfig(BaseModel):
    enabled: bool = False
    poll_interval_seconds: int = 30
    # API base for DNS endpoints. Defaults to derive from ingest_url's
    # origin so most operators don't have to set this twice.
    api_base: str | None = None
    servers: list[DnsServerConfig] = Field(default_factory=list)
    # How often to scrape CoreDNS Prometheus metrics. Independent of
    # bundle polling because we don't want the bundle cadence to
    # dictate metrics resolution (or vice-versa).
    metrics_interval_seconds: int = 60
    # Whether to scrape metrics at all. Set to false on air-gapped
    # collectors where outbound metrics push isn't wanted.
    metrics_enabled: bool = True


class CollectorConfig(BaseModel):
    collector_id: UUID
    site_id: UUID
    ingest_url: str
    heartbeat_interval_seconds: int = 30
    buffer_path: str = "/var/lib/dcim-collector/buffer.db"
    api_token_file: str | None = None
    mtls: MtlsConfig = Field(default_factory=MtlsConfig)
    devices: list[DeviceConfig] = Field(default_factory=list)
    syslog_listen: int | None = None  # bind port for syslog ingest
    dns: DnsAgentConfig = Field(default_factory=DnsAgentConfig)

    @classmethod
    def load(cls, path: str | Path) -> CollectorConfig:
        with open(path) as fh:
            raw: dict[str, Any] = yaml.safe_load(fh) or {}
        return cls.model_validate(raw)
