# Deployment

## Local development

Requires Docker Desktop (with Compose v2), Node 20+, Python 3.12+.

```bash
make up
make migrate
make seed
```

Access:
- API + OpenAPI: http://localhost:8000/docs
- Frontend: http://localhost:5173
- Elasticsearch: http://localhost:9200
- Postgres: localhost:5432 (user: dcim, db: dcim)

## Site collector (lab)

Build and run a collector pointed at the local API:

```bash
cd collector
python -m venv .venv && .venv/bin/pip install -e .
DCIM_INGEST_URL=https://localhost:8443 \
DCIM_COLLECTOR_TOKEN=$(cat ./sample-token) \
python -m dcim_collector.main --config sample-config.yaml
```

In production, collectors deploy as either:
- A **systemd unit** on a hardened RHEL/Ubuntu jump host inside the site, or
- A **container** on a small Kubernetes/k3s cluster at the site.

Each collector has:
- Its own mTLS client certificate issued by the central CA
- A scoped API token for inventory sync
- A persistent SQLite buffer at `/var/lib/dcim-collector/buffer.db`

## Production (Kubernetes)

Helm chart lives at `infra/helm/dcim/`. Provides:

- `api` Deployment + HPA + Service
- `worker` Deployment + HPA
- `ingest` Deployment + Service (mTLS terminated here)
- `frontend` Deployment + Service
- `migrations` Job (run on upgrade)
- `postgresql` subchart hook (or external HA cluster)
- `elasticsearch` subchart hook (or external)
- `redis` subchart hook
- Ingress with TLS
- ServiceMonitor / PodMonitor for Prometheus scraping
- NetworkPolicy templates restricting east-west traffic

```bash
helm upgrade --install dcim infra/helm/dcim \
  --namespace dcim --create-namespace \
  -f my-values.yaml
```

See `infra/helm/dcim/values.yaml` for the full surface.

## Air-gapped / STIG notes

- All container images must be mirrored to an internal registry. The chart accepts `image.repository` overrides for every component.
- Collector binaries can be packaged as RPM/DEB for offline install.
- Audit logs are configurable to ship to an external SIEM (Splunk, Elastic, Sentinel) via syslog or HTTP.
