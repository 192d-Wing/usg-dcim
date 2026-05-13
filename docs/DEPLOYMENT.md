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
- `postgresql` subchart hook (or external HA cluster) — requires the
  TimescaleDB extension; the `timescale/timescaledb-ha` image is the
  drop-in production-grade choice
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
on first start. It ships with two users (`demo` / `demo` and
`dcim_admin` / `dcim_admin`, both with the `dcim-admin` realm role)
and the client `dcim-spa` wired with secret `dev-secret-change-me`,
the redirect URIs the API expects, and the realm-role mapper that
emits `realm_access.roles` into the ID token. Direct access grants
are enabled on `dcim-spa` so the validation script below can exercise
the full chain without driving a browser; production swaps the
realm seed for one with direct grants off.

Verify the discovery doc is reachable:

```bash
curl -s http://localhost:8080/realms/dcim/.well-known/openid-configuration | jq .issuer
```

End-to-end SSO → capability mapping smoke (mint a token through
`dcim-spa`, validate audience + issuer + JWKS signature, resolve
capabilities through `oidc_role_mappings`):

```bash
ID=$(curl -sfS -X POST \
  -d client_id=dcim-spa \
  -d client_secret=dev-secret-change-me \
  -d username=dcim_admin -d password=dcim_admin \
  -d grant_type=password -d scope=openid \
  http://localhost:8080/realms/dcim/protocol/openid-connect/token \
  | jq -r .id_token)

docker exec -e ID_TOKEN="$ID" docker-api-1 python - <<'PY'
import asyncio, os, httpx
from jose import jwt
from dcim.settings import get_settings
from dcim.db import async_session
from dcim.security.scope import caps_from_idp_roles

async def main():
    s = get_settings()
    async with httpx.AsyncClient() as c:
        meta = (await c.get(f"{s.oidc_issuer}/.well-known/openid-configuration")).json()
        jwks = (await c.get(meta["jwks_uri"])).json()
    claims = jwt.decode(
        os.environ["ID_TOKEN"], jwks, algorithms=["RS256"],
        audience=s.oidc_client_id, issuer=s.oidc_issuer,
        options={"verify_at_hash": False},
    )
    roles = claims.get("realm_access", {}).get("roles", [])
    async with async_session() as db:
        caps = await caps_from_idp_roles(db, roles)
        print("user:", claims["preferred_username"],
              "roles:", [r for r in roles if r.startswith("dcim")],
              "caps:", list(caps))
asyncio.run(main())
PY
```

Expected output for `dcim_admin` (EnterpriseAdmin):

```text
user: dcim_admin roles: ['dcim-admin'] caps: ['*']
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
  Helm's wait flag (`--wait --timeout=10m`) is recommended. The
  Postgres image MUST have the TimescaleDB extension preinstalled
  (`timescale/timescaledb-ha:pg16` or equivalent) — migration 0046
  fails on stock Postgres.
- The ServiceMonitor template assumes the Prometheus operator CRDs
  are installed in-cluster. Skip with `--set
  serviceMonitor.enabled=false` if not.

## Air-gapped / STIG notes

- All container images must be mirrored to an internal registry. The chart accepts `image.repository` overrides for every component.
- Collector binaries can be packaged as RPM/DEB for offline install.
- Audit logs are configurable to ship to an external SIEM (Splunk, Elastic, Sentinel) via syslog or HTTP.
