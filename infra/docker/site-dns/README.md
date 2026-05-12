# Site DNS bundle

Brings up the on-site CoreDNS deployment driven by central DCIM:

- **collector** — polls `/api/v1/dns/servers/{id}/bundle` for each
  configured `DnsServer`, writes Corefile + zone files (+ `gobgp.yaml`
  when recursive) into the shared `dns-state` volume, and signals
  reloads.
- **coredns-auth** — authoritative for the per-site zone (and loads
  the fabric-wide bundle for resilience). Listens on the management IP.
- **coredns-recursive** — forwards `*.<fabric_apex>` to the local
  authoritative pod and everything else to operator upstreams. Listens
  on the per-fabric anycast IP via host networking.
- **gobgp** — advertises the anycast `/32` and `/128` (when set) to
  the BGP peers DCIM has configured for this site.

## Running it locally next to central

The compose project is named `site42` so it doesn't clash with the
central `docker` project on the same Docker engine.

### 1. In central, create the rows the site needs

Through the UI or the API, with `inventory:write`:

- A **Fabric** + a **VRF** (default VRF is auto-created).
- A **Site** (`IPAM → Sites`).
- An **AnycastGroup** for the fabric (`IPAM → DNS → Anycast groups`).
  Set `anycast_ipv4` to something safe on your lab subnet.
- A **BGP peer** at the site (`IPAM → BGP peers → Peers`). Local AS
  defaults to the seeded `AS 4200000000`; peer AS is whatever your
  leaf/ToR speaks.
- Two **DnsServer** rows: one `auth`, one `recursive`. Bind the
  recursive one to the AnycastGroup + a BGP peer via the
  `Announced to peer` Multiselect on the DNS server Edit modal.
- A **Collector** row (`Collectors`) → take the issued bearer token.

Note the UUIDs of: the Collector, the Site, the auth `DnsServer`,
and the recursive `DnsServer`.

### 2. Drop the config + token into this directory

```bash
cp collector.yaml.example collector.yaml
# fill in the four UUIDs in collector.yaml
echo "$ISSUED_BEARER_TOKEN" > token
chmod 0600 token
```

### 3. Bring it up

```bash
docker compose -p site42 \
  -f infra/docker/site-dns/docker-compose.yml \
  up -d --build
```

`-p site42` is the project name — change it per site so multiple site
stacks can coexist on one Docker host.

### 4. Verify

```bash
# Collector log should show dns_bundle_applied within ~30s
docker compose -p site42 -f infra/docker/site-dns/docker-compose.yml \
  logs -f collector | grep -iE "bundle|render"

# CoreDNS auth answers a record from a site zone
dig @127.0.0.1 -p 5353 leaf-01.site42.prod.dcim.mil

# Recursive forwards an external name
dig @<anycast-ip> example.com
```

## How the pieces talk

```text
                  central DCIM (compose project "docker")
                  ┌───────────────────────────────────┐
                  │  api: 0.0.0.0:8000                │
                  └────────────────┬──────────────────┘
                                   │ http(s)
                  host.docker.internal:8000
                                   │
   site42 compose project          │
   ┌───────────────────────────────┼───────────────────┐
   │ collector ────polls /dns/...─►│                   │
   │     │                                             │
   │     │ writes Corefile + zones + gobgp.yaml        │
   │     ▼                                             │
   │ /var/lib/dcim-dns/  (shared volume)               │
   │     │                                             │
   │     ├── auth/         ───► coredns-auth           │
   │     └── recursive/    ───► coredns-recursive      │
   │                       ───► gobgp                  │
   └───────────────────────────────────────────────────┘
```

The collector container reaches central via `host.docker.internal` —
the `extra_hosts: host-gateway` mapping in the compose file is what
makes that DNS name resolve on Linux. On macOS/Windows Desktop the
mapping is built-in but the entry is harmless.

The four service containers share the host PID namespace so the
collector can `kill -SIGUSR1 <coredns-pid>` to trigger zone reload
and `kill -SIGHUP <gobgp-pid>` to trigger BGP config reload. PIDs are
read from the pidfiles each container drops on the shared volume.

## Auth

The collector uses the existing enrollment flow — it presents the
bearer token from `/etc/dcim/token` on every request to
`ingest_url` and `/api/v1/dns/...`. The CoreDNS / GoBGP containers
don't talk to central directly; they only read the bundle the
collector dropped on the shared volume. Single credential, single
audit trail.

## Hickory for the recursive (opt-in)

The recursive resolver supports a Hickory DNS variant alongside the
default CoreDNS build — useful at sites with high client counts (30k+
per site) where Hickory's no-GC latency tail and lower per-core CPU
matter. The authoritative pod stays on CoreDNS either way.

Tested against Hickory `0.26.0` (`hickorydns/hickory-dns:latest` as of
2026-05-12). The renderer emits the
`zone_type = "External"` forwarder shape; older `Forward` zone-type
configs from pre-0.26 drafts are not compatible.

### Cutover

1. In central, flip the fabric to Hickory:

   ```bash
   curl -X PATCH /api/v1/ipam/fabrics/$FABRIC_ID \
     -H "content-type: application/json" \
     -d '{"recursive_engine":"hickory"}'
   ```

   The next bundle poll renders a TOML config instead of a Corefile.

2. Layer the Hickory overlay on the compose bring-up:

   ```bash
   docker compose -p site42 \
     -f infra/docker/site-dns/docker-compose.yml \
     -f infra/docker/site-dns/docker-compose.hickory.yml \
     up -d
   ```

The collector recognizes the bundle's engine hint (`coredns` vs
`hickory`), writes the matching filename (`Corefile` vs
`config.toml`) into the recursive output_dir, and signals SIGHUP
instead of SIGUSR1 on reload.

### Verify

Three checks confirm a healthy cutover. Run them from any host that
can reach the recursive pod (anycast IP, or directly on the per-site
management IP):

```bash
# 1. External recursion succeeds — proves catch-all upstream config.
dig +short @<recursive-ip> example.com A

# 2. Apex stub forwards to local auth — proves the fabric-apex zone
#    block points at the right CoreDNS-auth pod.
dig @<recursive-ip> SOA <fabric_apex>

# 3. Collector applied the bundle — `last_render_status` flips to ok
#    and `last_render_etag` matches the latest bundle.
curl /api/v1/dns/servers/$RECURSIVE_ID | jq '{last_render_status, last_render_etag}'
```

Smoke-test the rendered TOML offline without touching the live stack:

```bash
docker run --rm -v /etc/dcim-dns/recursive:/cfg:ro \
  hickorydns/hickory-dns:latest \
  --config /cfg/config.toml --validate
```

`--validate` exits 0 only when Hickory can parse every zone block,
which catches schema drift the moment the upstream image version
moves.

### Rollback

Flip the fabric back to `coredns` and re-run with the base compose
file only — no Hickory state survives:

```bash
curl -X PATCH /api/v1/ipam/fabrics/$FABRIC_ID \
  -H "content-type: application/json" \
  -d '{"recursive_engine":"coredns"}'

docker compose -p site42 \
  -f infra/docker/site-dns/docker-compose.yml \
  up -d --force-recreate coredns-recursive
```

The collector cleans up the stale `config.toml` on its next poll and
re-writes `Corefile` in place.

## Production

The compose stack is the local development surface. Production
deployments use the per-site collector + CoreDNS pods deployed via
the Helm chart pattern in `infra/helm/`. The same `dns:` block in
the collector config drives both.
