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
- Postgres: localhost:5432 (user: dcim, db: dcim, TimescaleDB extension preinstalled)

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

Helm chart lives at `deploy/helm/dcim/`. Provides:

- `api` Deployment + HPA + Service
- `worker` Deployment + HPA
- `ingest` Deployment + Service (mTLS terminated here)
- `frontend` Deployment + Service
- `migrations` Job (run on upgrade)
- `postgresql` subchart hook (or external HA cluster) — requires the
  TimescaleDB extension preloaded via
  `shared_preload_libraries = 'timescaledb'`. The
  `timescale/timescaledb-ha` image sets this on first init; if you are
  upgrading an existing Postgres volume that did NOT have the extension
  preloaded, run `ALTER SYSTEM SET shared_preload_libraries =
  'timescaledb'` and restart the cluster BEFORE applying migration 0046,
  or the CREATE EXTENSION call dies mid-statement
- `redis` subchart hook
- Ingress with TLS
- ServiceMonitor / PodMonitor for Prometheus scraping
- NetworkPolicy templates restricting east-west traffic

```bash
helm upgrade --install dcim deploy/helm/dcim \
  --namespace dcim --create-namespace \
  -f my-values.yaml
```

See `deploy/helm/dcim/values.yaml` for the full surface.

### Cilium BGP exposure (optional)

Set `bgp.enabled=true` and the chart emits four Cilium CRDs to advertise
`type: LoadBalancer` Service IPs to upstream routers:

- `CiliumBGPPeerConfig` — shared timers + AFI/SAFI families
- `CiliumBGPClusterConfig` — local ASN + peer list
- `CiliumBGPAdvertisement` — Service-label selector + `LoadBalancerIP`
- `CiliumLoadBalancerIPPool` — IP range(s) Cilium hands out

Minimum opt-in values:

```yaml
bgp:
  enabled: true
  localASN: 65000
  peers:
    - { name: rtr-a, address: "2001:db8:1::1", asn: 65001 }
    - { name: rtr-b, address: "2001:db8:1::2", asn: 65001 }
  ipPools:
    - name: public-v6
      blocks:
        - { cidr: "2001:db8:1:ffff::/120" }

otter:
  service:
    type: LoadBalancer
    labels:
      dcim.io/bgp-advertise: "true"   # NOTE: quote the string
finch:
  service:
    type: LoadBalancer
    labels:
      dcim.io/bgp-advertise: "true"
heron:
  service:
    type: LoadBalancer
    labels:
      dcim.io/bgp-advertise: "true"
```

The CRD apiVersion is `cilium.io/v2alpha1` — same as the regional
cluster renderer in
[`packages/otter/src/dcim/regiondeploy/cilium.py`](../packages/otter/src/dcim/regiondeploy/cilium.py)
so central and regional clusters speak the same dialect; bump in
lockstep with that file when Cilium graduates the API.

Quote `"true"` (and any other label value) — unquoted YAML `true`
becomes a boolean and Kubernetes rejects the Service label.

The default `bgp.advertise.selectorLabels` is
`{ dcim.io/bgp-advertise: "true" }`. Override it to advertise a
different label key, or `selectorLabels: {}` to advertise every LB
Service in the namespace.

### Cilium L2 announcement fallback (PR 89)

For sites whose upstream is L2-only (no BGP-capable router), set
`l2.enabled=true` instead of `bgp.enabled`. The two are mutually
exclusive — setting both fails `helm template` so an operator
can't accidentally double-advertise.

L2 mode emits two CRDs:

- `CiliumL2AnnouncementPolicy` — gratuitous-ARP / NDP advertises
  LoadBalancer Service IPs onto the chosen L2 segment.
- `CiliumLoadBalancerIPPool` — same shape as `bgp.ipPools`; the
  pool gives out IPs the L2 policy announces.

The selector label is the **same** as BGP mode
(`dcim.io/bgp-advertise: "true"`) so flipping `bgp.enabled →
l2.enabled` swaps the announcement mechanism without touching the
Service definitions in the subcharts or the `dns-site` /
`dhcp-site` charts.

Minimum opt-in values:

```yaml
bgp:
  enabled: false   # ensure BGP is OFF
l2:
  enabled: true
  ipPools:
    - name: lab-lan
      blocks:
        - { cidr: "192.168.10.240/29" }
  # Optional: pin to a subset of nodes that face the L2 segment.
  nodeSelector:
    role.dcim.io/lb-speaker: "true"

otter:
  service:
    type: LoadBalancer
    labels:
      dcim.io/bgp-advertise: "true"   # same label, different upstream
```

## Site DNS via Cilium BGP (PR 71)

`deploy/helm/dns-site/` deploys one CoreDNS pod per `DnsServer` row on
the site cluster, fronted by a `type: LoadBalancer` Service whose IP
is pinned to the matching `AnycastGroup` addresses via the
`io.cilium/lb-ipam-ips` annotation. The bundle pipeline
(`/api/v1/dns/servers/{id}/bundle`) is unchanged; the GoBGP sidecar is
replaced by Cilium BGP advertising the Service label
`dcim.io/bgp-advertise=true`.

Render values from a `DnsServer` + `AnycastGroup` row via
[`packages/otter/src/dcim/regiondeploy/dns_site.py`](../packages/otter/src/dcim/regiondeploy/dns_site.py)
(`render_dns_site_values(server, anycast_group=..., bundle_api_base_url=...)`).
See [`deploy/helm/dns-site/README.md`](../deploy/helm/dns-site/README.md)
for the install flow and required prerequisites (Cilium BGP, bundle
token Secret, optional private-CA Secret).

`AnycastBgpBinding` rows are no longer consumed by config generation
in the k8s-native path — they remain as audit/policy state. The
cluster's BGP peer list comes from `regiondeploy/cilium.py` (regional)
or the umbrella `bgp.peers` values (central).

## Site DHCP (Kea Control Agent) via Cilium BGP (PR 72)

`deploy/helm/dhcp-site/` exposes a Kea Control Agent — the HTTPS REST
endpoint DCIM stores in `DhcpServer.kea_url` — via a
`type: LoadBalancer` Service pinned to an anycast IP through
`io.cilium/lb-ipam-ips`. The `dcim.io/bgp-advertise=true` label causes
the cluster's `CiliumBGPAdvertisement` to announce the IP to upstream
routers. DHCPv6 unicast (UDP/547) optionally rides the same Service.

**DHCPv4 (UDP/67) is intentionally not anycast.** DHCPv4 broadcast
requires DHCP Relay (RFC 1542) at the router; point the relay's
`giaddr` / helper-address at the chart's anycast IP and Kea hears
the relayed unicast frame.

Render values from a `DhcpServer` row via
[`packages/otter/src/dcim/regiondeploy/dhcp_site.py`](../packages/otter/src/dcim/regiondeploy/dhcp_site.py)
(`render_dhcp_site_values(server, anycast_ips=[...], dhcpv6=...)`).
See [`deploy/helm/dhcp-site/README.md`](../deploy/helm/dhcp-site/README.md)
for the install flow.

Kea config (`kea-ctrl-agent.conf`) is operator-owned via a `ConfigMap`
the chart mounts. A future PR can mirror the DNS bundle pipeline at
`/api/v1/dhcp/servers/{id}/bundle` so DCIM authors Kea config the way
it authors Corefiles today.

## SSO smoke (Keycloak)

For end-to-end OIDC validation against a real issuer, bring up the
`sso` profile:

```bash
docker compose --profile sso up -d keycloak
```

The realm `dcim` is imported from
[`deploy/docker/keycloak-realm.json`](../deploy/docker/keycloak-realm.json)
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
[`docker-compose.yml`](../deploy/docker/docker-compose.yml). Hitting
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

[`collector-config.yaml`](../deploy/docker/collector-config.yaml) ships
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
helm dependency update deploy/helm/dcim
helm template dcim deploy/helm/dcim | kubectl apply --dry-run=client -f -
helm install dcim deploy/helm/dcim \
  --namespace dcim --create-namespace \
  -f deploy/helm/dcim/values-k3d.yaml
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

## Audit log immutability + WORM export

- Migration `20260524_0050_audit_log_immutable` installs `BEFORE UPDATE` and `BEFORE DELETE` triggers on `audit_log`. Any attempt by the application (or any non-superuser connection) to modify or delete a row raises `42501 insufficient_privilege`. This is on by default; no operator action required to enable.
- The triggers are tamper-evident at the DDL layer — disabling or dropping them requires superuser DDL, which `pg_audit` will capture if enabled on the cluster.
- For WORM compliance (FedRAMP / IL5 / NIST 800-53 AU-9): pair the on-database immutability with a scheduled export to an external object store with object-lock retention (S3 + Glacier compliance mode, Azure Blob immutable storage, etc.). The pipeline lives outside the database — a cron job that `COPY (SELECT * FROM audit_log WHERE occurred_at >= last_export) TO STDOUT` and uploads to the immutable bucket.
- Disaster recovery: the same export feeds the SIEM mention above, so audit data has at least two homes (the DB itself, plus the immutable store) and a tertiary if SIEM persistence is configured.

## Per-org tenancy: identifying sites without an `organization_id`

After migration `20260524_0051_sites_organization_fk` runs (PR 66), `sites.organization_id` is the new FK pointer onto `organizations.id`. The migration backfills it only where the legacy `sites.organization` string already exactly matches an `organizations.name`. Rows with no match keep `organization_id IS NULL`.

To find them and decide what to do:

```sql
SELECT s.id, s.code, s.name, s.organization AS legacy_string
FROM   sites s
WHERE  s.organization IS NOT NULL
  AND  s.organization_id IS NULL
ORDER  BY s.organization;
```

For each unique `legacy_string`, decide:

1. **Create the missing org row.** Insert into `organizations` with proper address + POC fields (these are NOT NULL — they can't be auto-defaulted). Then run the migration's backfill again or just `UPDATE sites SET organization_id = ... WHERE id = ...`.
2. **Map to an existing org under a different name.** Update the site directly: `UPDATE sites SET organization_id = '<uuid>' WHERE id = ...`. The legacy string is left untouched (informational).
3. **Leave the site un-orged.** The string column is retained either way; a NULL FK just means "not yet mapped." ABAC organization-scope still works against the string column today.

The legacy `sites.organization` string column will be retired in a follow-up PR once API consumers have migrated to the FK.
