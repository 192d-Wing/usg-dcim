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
    # Skip the prometheus scrape for this server. Hickory's official
    # image isn't built with the `prometheus` feature flag, so the
    # endpoint doesn't exist — set this to False on Hickory recursives
    # until the dnstap reader lands as the unified QPS source.
    metrics_enabled: bool = True
    # UNIX socket the resolver writes dnstap frames to. When set, the
    # collector spawns a dnstap listener and folds per-query (name,
    # type) tuples into a top-K reservoir shipped on the metrics POST.
    # Only CoreDNS auth pods support this today; Hickory has no
    # dnstap output. Set on the same shared volume as zones/keys so
    # both the resolver container and the collector see the same path.
    dnstap_socket: str | None = None
    # gobgpd gRPC endpoint for advertising the recursive's anycast
    # prefixes. Default `localhost:50051` works when the collector
    # joins the host network namespace alongside gobgpd; otherwise
    # operators point this at the host IP plus the API-host port the
    # gobgpd command-line exposes (`--api-hosts 0.0.0.0:50051`).
    # Empty string disables the advertise loop for this server even
    # when the bundle carries prefixes — useful for debugging or
    # for sites where the BGP fabric is managed out-of-band.
    gobgp_api_host: str = "localhost:50051"


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
    # Run DnsHealthCheck probes from this collector — useful when
    # target IPs live on a site network central can't reach. The
    # collector polls /dns/health-checks?fabric_id=<...> for the
    # check list and POSTs each probe result back; central's worker
    # naturally backs off once it sees fresh last_checked_at values.
    health_checks_enabled: bool = False
    # Which fabric's checks this collector probes. Must be set when
    # health_checks_enabled = true. A single collector can only
    # probe one fabric in v1.
    health_check_fabric_id: UUID | None = None
    # Top-level poll interval — how often we refresh the check list
    # from central. Each individual check fires on its own
    # interval_seconds within this cadence.
    health_check_poll_interval_seconds: int = 60


class CollectorConfig(BaseModel):
    collector_id: UUID
    site_id: UUID
    ingest_url: str
    # Optional override sending telemetry batches to a different host
    # than the rest of the API surface — used to point telemetry at
    # services/go-ingest while heartbeats, DNS bundle polling, etc.
    # continue against the Python api. Falls back to ingest_url when
    # unset, so existing deployments are unaffected.
    telemetry_url: str | None = None
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
