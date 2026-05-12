# DNS admin guide

For the engineer who owns the platform: deploys the central stack,
runs the migrations, manages secrets, bootstraps sites, sets the
retention and rotation policies, and gets paged when something
breaks. The day-to-day record-management and DNSSEC enable/disable
flow lives in the [operator guide](operator-guide.md); the code
internals are in the [implementation guide](implementation.md).

## What DCIM-DNS is

DCIM owns inventory (IPAM + assets) and pushes a rendered DNS
config to one or two CoreDNS pods per site. There is **no
zone-transfer fabric** — the central API renders Corefile + zone
files + GoBGP YAML on every collector poll, and the on-site
collector drops them onto a shared volume and signals CoreDNS to
reload. Operators never edit zone files; they edit DCIM records
through the UI/API and the renderer takes care of the rest.

See [design/dns-integration.md](../design/dns-integration.md) for
the decisions behind that shape (push/pull vs AXFR, two-pod
auth+recursive split, per-fabric anycast, etc.).

## Components an admin operates

| Component                  | Where it runs           | What admin does |
| -------------------------- | ----------------------- | --------------- |
| `api` (FastAPI)            | central k8s             | migrations, env config, capacity scaling |
| `worker` (arq)             | central k8s             | crons: zone projection, DNSSEC rotation, metric retention |
| Postgres `dns_*` tables    | central HA Postgres     | backups, key-secret rotation |
| `dns-agent` loop           | site `collector` pod    | enrollment + token issuance per site |
| `coredns-auth` pod         | site                    | image pull from ghcr, port/IP allocation |
| `coredns-recursive` pod    | site                    | anycast IP + leaf BGP peering |
| `gobgp` sidecar            | site (host networking)  | BGP MD5 / TCP-AO secrets, AS numbers |
| `nsec3sign` custom image   | ghcr.io                 | image builds + tag bumps |

## Prerequisites

- **Central stack already deployed** per [docs/DEPLOYMENT.md](../DEPLOYMENT.md).
- **Postgres reachable** from the API + worker; migration `0028`
  (NSEC3 columns) requires write access.
- **ghcr access** for the custom CoreDNS image — see the
  "Pulling the signing image" section below for the read-grant
  workflow.
- **A BGP-speaking leaf** at each site for anycast (DNS recursive
  only — auth is unicast).

## Initial setup

### 1. Run the DNS migrations

The DNS schema landed across several migrations under
`backend/src/dcim/migrations/versions/2026*_dns*.py` and
`...nsec3_params.py`. They're picked up automatically by `make
migrate` / `alembic upgrade head` on the API container's
init-container. No special order beyond running the full chain.

```bash
alembic -c backend/alembic.ini upgrade head
```

Verify the tables landed:

```bash
psql -c '\dt dns_*'
# dns_zones, dns_records, dns_servers, dns_keys, dns_views,
# dns_blocklists, dns_blocklist_entries, dns_forwarders,
# dns_health_checks, dns_server_metrics_samples, dns_events,
# anycast_groups, bgp_peers, anycast_bgp_bindings, dns_render_status
```

### 2. Set DNS-related environment variables

All settings carry sensible defaults; you only need to set the
ones that affect your deployment posture.

| Env var                              | Default                 | Why you'd change it |
| ------------------------------------ | ----------------------- | ------------------- |
| `DCIM_DNS_DNSSEC_SECRET`             | (unset → plaintext)     | Set to a Fernet key in any environment that holds production zone keys. Generate with `python -c "from cryptography.fernet import Fernet; print(Fernet.generate_key().decode())"`. **Once set, never lose it** — encrypted `DnsKey.private_pem` rows can't be decrypted without it. |
| `DCIM_DNS_DNSSEC_DEFAULT_ALGORITHM`  | `ecdsap256sha256`       | Switch to `ed25519` if your resolver fleet has been validated for it (smaller sigs, faster math). Stay on ECDSA unless you've tested. |
| `DCIM_DNS_DDNS_ENABLED`              | `true`                  | Set `false` if you don't want DHCP-lease churn driving DNS records. Static IPAM rows still project regardless. |
| `DCIM_DNS_METRICS_RETENTION_DAYS`    | `14`                    | The hourly cron drops `dns_server_metrics_samples` older than this. Bump only if you're driving long-horizon Grafana dashboards from the metrics table. |
| `DCIM_DNS_RECURSIVE_UPSTREAMS`       | `["1.1.1.1","8.8.8.8"]` | Set to your internal recursive (Active Directory DNS, an internal Unbound, etc.). Public defaults are dev-friendly but rarely correct in production. |
| `DCIM_DNS_ANYCAST_ORIGINATE_ASN`     | `4200000000`            | The 4-byte private ASN (RFC 6996) we announce anycast routes from. Override only if it collides with your internal ASN scheme. |

The Fernet secret is the one to be careful with. Document it in
your secret-store (Vault / sealed-secrets / GitOps with SOPS) with
**no rotation policy** unless you're prepared to migrate every
encrypted key row in one shot. Lazy re-encryption handles
plaintext-to-encrypted; encrypted-to-different-encrypted requires
the worker to decrypt-then-re-encrypt every row, which is a
deliberate operation.

### 3. Pulling the signing image (ghcr access)

The custom CoreDNS image
(`ghcr.io/192d-wing/coredns-nsec3sign:v1.11.3-N`) is a private
package — org policy disallows public ghcr packages. Each Docker
host that runs the site stack needs a one-time login:

```bash
# On every site host:
echo "$GHCR_PAT" | docker login ghcr.io -u <github-user> --password-stdin
```

The PAT must carry `read:packages` scope. Grant the relevant
operations team(s) read access via the package settings page —
`https://github.com/orgs/192d-Wing/packages/container/coredns-nsec3sign/settings`,
"Manage access" → "Invite teams or people" with role = Read.

For air-gapped sites without ghcr reachability, mirror the image
to your internal registry:

```bash
docker pull ghcr.io/192d-wing/coredns-nsec3sign:v1.14.2-1
docker tag ghcr.io/192d-wing/coredns-nsec3sign:v1.14.2-1 \
  internal.registry.example/dns/coredns-nsec3sign:v1.14.2-1
docker push internal.registry.example/dns/coredns-nsec3sign:v1.14.2-1
```

Then edit [infra/docker/site-dns/docker-compose.yml](../../infra/docker/site-dns/docker-compose.yml)
to point `coredns-auth.image` at the internal copy.

## Site bootstrap

The site-dns stack (collector + 2 CoreDNS pods + GoBGP) bring-up
is documented in [infra/docker/site-dns/README.md](../../infra/docker/site-dns/README.md).
Admin's job is to make the rows in central DCIM that the site
needs before the operator can light it up:

1. **Fabric + VRF** (IPAM page) — one per security domain or
   enclave.
2. **Site** — the physical location.
3. **AnycastGroup** — one per fabric for `service=dns_recursive`.
   The `anycast_ipv4` (and optional `anycast_ipv6`) must be a
   value the leaf is willing to accept route advertisements for.
4. **BgpPeer** rows at the site — local AS defaults to the
   seeded `4200000000`; peer AS is the leaf's. MD5 or TCP-AO
   keychain reference goes here.
5. **Two DnsServer rows** at the site — one `auth`, one
   `recursive`. Bind the recursive to the AnycastGroup + the BGP
   peer(s) via the UI's `Announced to peer` multiselect.
6. **Collector row** — generate the bearer token; drop it on the
   site host as `/etc/dcim/token` (0600).

Once those rows exist, the site operator clones the compose file,
fills in the four UUIDs (Site, Collector, two DnsServer ids), and
runs `docker compose up -d`. The collector polls `/dns/servers/{id}/bundle`
within 30 seconds and writes the rendered config; CoreDNS reloads
automatically.

### Anycast vs unicast

- **Auth pod** listens on the site's *management IP*. Operators
  query it directly for testing; the recursive pod's stub-zone
  forward points at it by IP. Anycast doesn't enter the auth path.
- **Recursive pod** binds the *anycast IP* via host networking.
  GoBGP announces a `/32` (v4) and optionally `/128` (v6) to the
  leaf. Multiple sites can announce the same anycast IP — the
  leaf picks the closest one by IGP cost.

### DNSSEC key rollover policy

ZSK rotation is policy-driven and operator-configurable per zone:

- `zsk_rotation_days = 0` → manual only (operator clicks Rotate
  ZSK from the DNSSEC tab).
- `zsk_rotation_days > 0` → the worker cron rotates the active
  ZSK every N days, retiring the previous one. Retired keys
  linger until the operator purges them so cached validators
  keep verifying through the SOA-expire window.

KSK rotation is always manual — it requires the operator to upload
a new DS record to the parent zone's operator. Don't try to
automate this; the parent-side coordination is what makes KSK
rotation slow regardless of how fast your tooling is.

Set a default rotation cadence in your runbook (e.g. 90 days for
ZSKs) and use the per-zone setting only for zones that need
something different.

## Monitoring

### Plugin metrics (auth pod)

The `coredns-nsec3sign` plugin emits five Prometheus metrics under
`coredns_nsec3sign_*` — names mirror the upstream `dnssec` plugin
where they overlap. Full table in
[infra/coredns-nsec3sign/README.md#metrics](../../infra/coredns-nsec3sign/README.md#metrics):

- `coredns_nsec3sign_cache_hits_total{server}`
- `coredns_nsec3sign_cache_misses_total{server}`
- `coredns_nsec3sign_cache_entries{server,type}`
- `coredns_nsec3sign_denials_total{server,type}`
- `coredns_nsec3sign_chain_entries{zone}`

Scrape from the `prometheus :9153` listener inside the auth pod.
Alert when the hit:miss ratio falls below ~5:1 sustained
(suggests cache thrash or operator-driven zone churn), or when
`chain_entries` for a zone drops sharply between reloads
(suggests the renderer dropped records — check the worker logs).

### Render-status metric (central)

The collector POSTs render-status back to `/dns/servers/{id}/render-status`
on every poll. Stale or failed renders show on the **Servers**
panel in the UI. Wire an alert from the DB:

```sql
SELECT id, last_render_status, last_render_error,
       last_render_at, EXTRACT(EPOCH FROM (now() - last_render_at)) AS age_s
FROM dns_servers
WHERE last_render_status != 'ok' OR last_render_at < now() - INTERVAL '5 minutes';
```

The collector polls every 30 s, so anything older than 2-3
minutes is a real fault (collector down, mTLS broken, API
unreachable from the site).

### Query metrics dashboard

CoreDNS exports per-zone query counts and latencies via the
`prometheus` plugin. The worker scrapes them every minute and
persists samples to `dns_server_metrics_samples` (retained per
`DCIM_DNS_METRICS_RETENTION_DAYS`). The DNS tab's per-server
panel renders these as a line chart; pre-built Grafana dashboard
JSON lives at `docs/dns/dashboards/` (when populated by the
release).

## Upgrades

### Plugin image

When a new `coredns-nsec3sign` tag ships:

1. Bump the tag in [infra/docker/site-dns/docker-compose.yml](../../infra/docker/site-dns/docker-compose.yml).
2. Roll one site at a time: `docker compose -p siteN pull coredns-auth && docker compose -p siteN up -d coredns-auth`.
3. Watch `coredns_nsec3sign_chain_entries` for that zone — should
   recover to its previous value within 30 s of the restart (the
   plugin re-parses the zone file at boot).
4. Run a probe: `dig +dnssec @<site-IP> -p <auth-port>
   missing.<zone> A` → expect NXDOMAIN with NSEC3 records signed
   by the existing keys.

### Backend (DCIM)

Standard rolling upgrade via the Helm chart. Migrations are an
init-container; the chart blocks the new API pods until migrations
complete. DNS-specific concern: the renderer is a pure function in
`backend/src/dcim/services/dns.py` — render output should be
identical across patch versions for the same input. If a render
diff shows up on a deploy without intentional renderer changes,
that's a regression — back out and bisect.

## Backup and restore

### Key material

`DnsKey.private_pem` is the irreplaceable asset. Backup posture:

1. **Postgres logical backups** capture key rows along with
   everything else. Encrypted-at-rest if `DCIM_DNS_DNSSEC_SECRET`
   is set; plaintext otherwise. Treat the dumps as secret-grade
   either way.
2. **Fernet secret backup** lives in your secret-store. Without
   it the encrypted dumps are useless. Verify the recovery path
   *before* you need it — a drill once per cert-rotation cycle is
   enough.

### Zone state

Zones, records, servers, forwarders, blocklists, anycast groups,
and BGP peers are all in Postgres. Standard backup covers them.
On restore, the renderer produces the same bundle deterministically
— sites pick up the restored state on the next collector poll
without further action.

### Site state

The site-dns volume (`dns-state`) is **ephemeral**. Rendered
configs land there from the central API; losing the volume means
the next collector poll re-renders into it. No backup needed.

## Troubleshooting

### "Validators are bogus on every DNSSEC response"

- Check the parent zone's DS record matches the KSK's DS as shown
  on the DNSSEC tab. The most common cause is a stale parent-side
  DS after a KSK rotation that the operator didn't follow up on.
- Check `dig DNSKEY @<auth-IP>` and confirm the DNSKEY RRset has
  RRSIGs. If not, keys aren't loaded — look at the auth pod logs
  for `nsec3sign: loading <path>` errors.
- Verify the auth pod is running the `coredns-nsec3sign` image,
  not stock `coredns/coredns`. The `dnssec` plugin doesn't emit
  NSEC3 — NSEC3-flagged zones served by stock CoreDNS produce
  invalid denials.

### "Bundle render fails for a zone"

The collector logs `dns_bundle_apply_failed`. The render error is
also in `dns_servers.last_render_error`. Common causes:

- A `DnsRecord` row with malformed `data` JSON for its type — the
  renderer rejects it. Look for the record by `last_render_error`
  message; the UI lets you edit/delete.
- Zone has DNSSEC enabled but `DCIM_DNS_DNSSEC_SECRET` is unset
  and the stored `private_pem` is encrypted (legacy state). Set
  the secret, restart the worker, and retry.

### "Anycast IP is unreachable from clients"

- Check the GoBGP container is up: `docker compose -p siteN ps`.
- Confirm BGP session is established on the leaf — `show bgp
  summary` on the switch.
- Check the rendered `gobgp.yaml` includes the right anycast
  prefix and peer AS. The UI's Servers panel shows the last
  rendered config.

### "Plugin won't load on the auth pod"

The pod logs `nsec3sign: ...` at startup. Common faults:

- `salt must be hex` — operator hand-edited the Corefile to put
  non-hex in `salt`. Don't hand-edit; let the renderer produce
  it. (If the renderer is producing bad output, that's a backend
  bug — file an issue.)
- `loading <basename>: open: ...` — key file path wrong on disk,
  usually because the volume mount differs from what the renderer
  computed. Confirm the compose's `dns-state` volume mount path.
- `no SOA record found` — zone file missing the apex SOA. Look at
  the rendered `<zone>.zone` to confirm; the bundle assembly
  should always emit SOA at the apex.

## Pointers

- Code internals: [implementation.md](implementation.md)
- Day-to-day workflows: [operator-guide.md](operator-guide.md)
- Design context: [../design/dns-integration.md](../design/dns-integration.md)
- Site bring-up: [infra/docker/site-dns/README.md](../../infra/docker/site-dns/README.md)
- Plugin internals: [infra/coredns-nsec3sign/README.md](../../infra/coredns-nsec3sign/README.md)
- Security review: [infra/coredns-nsec3sign/SECURITY-REVIEW.md](../../infra/coredns-nsec3sign/SECURITY-REVIEW.md)
