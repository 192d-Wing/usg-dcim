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

## SSO smoke (Keycloak)

For end-to-end OIDC validation against a real issuer, bring up the
`sso` profile:

```bash
docker compose --profile sso up -d keycloak
```

The realm `dcim` is imported from
[`infra/docker/keycloak-realm.json`](../infra/docker/keycloak-realm.json)
on first start. It ships with one user (`demo` / `demo`) and the
client `dcim-spa` already wired with secret `dev-secret-change-me` and
the redirect URIs the API expects.

Verify the discovery doc is reachable:

```bash
curl -s http://localhost:8080/realms/dcim/.well-known/openid-configuration | jq .issuer
```

The API is pre-pointed at the Keycloak service in
[`docker-compose.yml`](../infra/docker/docker-compose.yml). Hitting
`GET /api/v1/auth/oidc/login` redirects to the realm authorization
endpoint; after authenticating, Keycloak posts the code to
`/api/v1/auth/oidc/callback`, the API validates the ID token via JWKS,
upserts the User row (matching by `sso_subject`, falling back to email),
and returns a short-lived JWT the SPA carries on with.

Production swaps `oidc_issuer` for the real Keycloak / Azure AD URL
and provisions a non-public client with a stronger secret.

## Site collector smoke (snmpsim)

The `collector` profile in compose now includes
[`snmpsim`](https://github.com/etingof/snmpsim), a synthetic SNMP agent
on UDP/1161, so the polling loop has something real to talk to without
rack hardware.

```bash
docker compose --profile collector up -d snmpsim collector
docker compose logs -f collector
```

[`collector-config.yaml`](../infra/docker/collector-config.yaml) ships
with a sample SNMP device pointed at `snmpsim:1161` (community
`public`) polling `sysUpTime` every 30s. After running `make seed`,
replace the placeholder `asset_id` with a real `Asset.id` from the
seeded inventory and the collector will start posting samples to
`/api/v1/ingest/telemetry`. Watch them land via:

```bash
curl -s 'http://localhost:9200/dcim-telemetry-*/_search?pretty' | jq .hits.total
```

Freshness flips from `unknown` to `current` on the asset detail page
once the first sample arrives.

## Helm + k3d smoke

For local end-to-end validation of the chart, use a k3d cluster:

```bash
k3d cluster create dcim --port "8080:80@loadbalancer"
helm dependency update infra/helm/dcim
helm template dcim infra/helm/dcim | kubectl apply --dry-run=client -f -
helm install dcim infra/helm/dcim \
  --namespace dcim --create-namespace \
  -f infra/helm/dcim/values-k3d.yaml
kubectl -n dcim wait --for=condition=available --timeout=300s deploy --all
```

CI runs `helm lint` and `helm template` on every PR (see
[`.github/workflows/ci.yml`](../.github/workflows/ci.yml)); a real
`helm install` against k3d runs locally — there is no GHA runner with
a persistent cluster, so end-to-end install is gated on a manual
smoke before tagging a release.

Common gotchas:
- The `migrations` Job needs the Postgres subchart healthy first;
  Helm's wait flag (`--wait --timeout=10m`) is recommended.
- Elasticsearch's default JVM heap is too large for k3d on a laptop;
  override with `elasticsearch.esJavaOpts=-Xms512m -Xmx512m` in
  values.
- The ServiceMonitor template assumes the Prometheus operator CRDs
  are installed in-cluster. Skip with `--set
  serviceMonitor.enabled=false` if not.

## Air-gapped / STIG notes

- All container images must be mirrored to an internal registry. The chart accepts `image.repository` overrides for every component.
- Collector binaries can be packaged as RPM/DEB for offline install.
- Audit logs are configurable to ship to an external SIEM (Splunk, Elastic, Sentinel) via syslog or HTTP.
