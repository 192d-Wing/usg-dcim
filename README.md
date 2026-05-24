# USG DCIM

Enterprise Data Center Infrastructure Management platform for 184+ datacenters, communications rooms, edge rooms, and geographically distributed sites.

## What this is

A multi-site DCIM with:

- **Centralized inventory & governance** in PostgreSQL (Region → Site → Building → Room → Row → Rack → Asset).
- **Distributed telemetry collection** via lightweight site collectors (SNMP, Redfish, Modbus TCP, REST, IPMI, syslog) with local store-and-forward over WAN outages.
- **High-volume telemetry** in a TimescaleDB hypertable (`telemetry_samples`) with monthly chunks, columnar compression after 7 days, 24-month retention, and freshness tracking.
- **RBAC + ABAC** scoping by region, site, organization, MAJCOM, mission, and enclave.
- **Enterprise dashboards** with global rollups and per-site drill-down.
- **Alerting at scale** with dedup, correlation, suppression, maintenance windows, and collector-down detection.
- **Bulk import/export** APIs and OpenAPI-documented integrations.

## Repository layout

```
usg-dcim/
├── packages/                  Animal-named components (see WORKSPACE.md)
│   ├── otter/                 FastAPI app — central API, workers, auth, alerting
│   ├── finch/                 React + TypeScript + Vite dashboard
│   ├── badger/                Go SNMP/Redfish/Modbus/REST/IPMI site collector
│   ├── heron/                 Go telemetry ingest service
│   ├── magpie/                Go alert evaluation service
│   ├── beagle/                Go DNS health probe
│   ├── mole/                  Legacy Python collector (deprecated)
│   └── wolf/                  CoreDNS NSEC3 plugin + Hickory Prometheus exporter
├── deploy/
│   ├── docker/                Local dev compose
│   ├── helm/                  Kubernetes/Helm chart for production
│   └── k8s/                   Raw manifests / kustomize bases
├── docs/                      Architecture, deployment, API guides
└── Makefile                   Top-level dev tasks
```

## Quick start (local dev)

```bash
make up          # docker-compose with postgres (TimescaleDB), redis, api, worker, frontend
make migrate     # apply Alembic migrations
make seed        # seed a small enterprise (3 regions, 6 sites, racks, devices)
make collector   # run a sample collector against the seeded site
```

API: <http://localhost:8000> (OpenAPI at `/docs`)
Frontend: <http://localhost:5173>
Postgres: `localhost:5432` (user `dcim`, db `dcim`, TimescaleDB extension)

## Deployment

Production deploys via Helm to Kubernetes. See [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md).
Architecture details: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).
