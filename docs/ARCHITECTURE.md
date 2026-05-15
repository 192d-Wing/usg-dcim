# Architecture

## Goals

1. Operate across **184+ sites** with centralized governance and distributed collection.
2. Tolerate **WAN outages** at any site without losing telemetry.
3. Separate the **inventory plane** (low-volume, transactional) from the **telemetry plane** (high-volume, time-series).
4. Provide **enterprise-wide visibility** while enforcing **per-site/per-enclave authorization**.
5. Scale horizontally on **Kubernetes** in production.

## Component diagram (logical)

```
                 ┌───────────────────────────────────────────────────┐
                 │                   Central Cluster                 │
                 │                                                   │
   Operators ──▶ │  React UI ──▶ FastAPI API ──▶ PostgreSQL (HA)     │
                 │                  │     ▲                          │
                 │                  ▼     │                          │
                 │               Workers (arq) ──▶ Redis             │
                 │                  │                                │
                 │                  ▼                                │
                 │            OpenSearch (telemetry, events)         │
                 │                  ▲                                │
                 │             Ingest Service (mTLS, signed tokens)  │
                 └──────────────────▲────────────────────────────────┘
                                    │ outbound only, mTLS
                ┌───────────────────┼───────────────────┐
                │                   │                   │
        ┌───────┴───────┐   ┌───────┴───────┐   ┌───────┴───────┐
        │  Site A       │   │  Site B       │   │  Site N (184+)│
        │  Collector    │   │  Collector    │   │  Collector    │
        │  + SQLite     │   │  + SQLite     │   │  + SQLite     │
        │  buffer       │   │  buffer       │   │  buffer       │
        │               │   │               │   │               │
        │ SNMP/Redfish/ │   │ SNMP/Redfish/ │   │ SNMP/Redfish/ │
        │ Modbus/REST/  │   │ Modbus/REST/  │   │ Modbus/REST/  │
        │ IPMI/syslog   │   │ IPMI/syslog   │   │ IPMI/syslog   │
        └───────────────┘   └───────────────┘   └───────────────┘
```

## Data planes

### Inventory plane (PostgreSQL — source of truth)

- Region, Site, Building, Room, Row, Rack, Asset (PDU, sensor, UPS, CRAC, switch, server, etc.)
- Cables, circuits, power feeds, RU positions
- Users, roles, permissions, scopes (RBAC + ABAC)
- API tokens, scoped to site/role/integration
- Alert rules (enterprise + site overrides), maintenance windows, suppressions
- Audit log (every enterprise-impacting action)
- Collector registry (id, site, mTLS cert fingerprint, last seen)

Indexes are designed around: `site_id`, `rack_id`, `device_id`, `collector_id`, `timestamp`, alert state, lifecycle state.

### Telemetry plane (OpenSearch)

- `dcim-telemetry-{site_id}-{yyyy-MM}` indices, ILM-managed (rollover, warm, cold, delete)
- `dcim-events-{yyyy-MM}` for syslog/SNMP traps
- Rollups (hourly, daily) written to `dcim-rollup-*` for long-horizon dashboards
- Each document carries `site_id`, `collector_id`, `device_id`, `metric`, `value`, `unit`, `ts`, `received_at`

### Cache / queue

- Redis backs **arq** queues for: scheduled polling sync, alert evaluation, report generation, bulk imports.
- Redis also caches RBAC scope expansions and dashboard rollups.

## Scopes & authorization

- **Authn:** OIDC (Keycloak/Azure AD) primary; SAML pluggable. Local fallback for break-glass.
- **Authz:** Roles grant capabilities; **scopes** restrict targets.
  - Capabilities: `inventory:read`, `inventory:write`, `power:control`, `alerts:ack`, `audit:read`, etc.
  - Scopes: any combination of `region_id`, `site_id`, `org`, `enclave`, `mission`, plus a set-based `site_group_id`.
- **Power-control** capabilities are separately permissioned and may require dual-approval (configurable per site).
- **API tokens** carry their own scope subset (cannot exceed user's scope).

## Resiliency

- Site collectors are **outbound-only**. They authenticate to the central ingest endpoint via mTLS (preferred) or signed JWT.
- Each collector maintains a **local SQLite buffer**. On WAN outage, polling continues, samples accumulate, and the forwarder drains them on reconnect with idempotent batch posts.
- Central UI surfaces **freshness** per device: `current` / `stale` / `estimated` / `manual`. A collector-down alert fires when `last_seen > threshold` (per-site default).

## Horizontal scaling

| Component        | Scaling model                          |
|------------------|----------------------------------------|
| API              | Stateless, behind ingress; HPA on CPU  |
| Workers          | Stateless arq workers; HPA on queue depth |
| Ingest           | Stateless; sticky-less; mTLS terminated at ingress or app |
| PostgreSQL       | HA (Patroni / cloud-managed); read replicas for reports |
| OpenSearch       | 3+ master, N data, hot/warm/cold tiers |
| Redis            | Sentinel or cluster                    |
| Object storage   | S3-compatible for reports/exports/imports |

## Future deliverables

- Helm chart with values for air-gapped/STIG-hardened deployments.
- BGP/anycast for collector ingest in multi-region central deployments.
- Streaming replication of Postgres audit logs to a separate WORM store for FedRAMP/IL controls.
