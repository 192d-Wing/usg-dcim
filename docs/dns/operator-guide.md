# DNS operator guide

For the day-to-day operator: how to create zones, manage records,
enable DNSSEC, flip to NSEC3, register CoreDNS servers, watch
health, and resolve common problems from the UI and API. Platform
deployment + secret management is in the [admin
guide](admin-guide.md); the code internals are in the
[implementation guide](implementation.md).

## Where DNS lives in the UI

The DNS tab sits inside the IPAM page (`IPAM → DNS`). Everything
DNS-related scopes to a **fabric** — pick one from the fabric
selector at the top before drilling in. Sub-views inside the tab:

| Sub-view              | Manages                                          |
| --------------------- | ------------------------------------------------ |
| Zones                 | Apex (per-fabric) + site (per-site) + reverse zones |
| Records               | A/AAAA/PTR (auto from IPAM) and CNAME/MX/TXT/SRV/NS/CAA (manual) |
| Servers + anycast     | CoreDNS deployments, BGP peers, anycast groups   |
| Forwarders            | Per-fabric conditional + catch-all upstreams     |
| Blocklists (RPZ-lite) | Sinkholes for known-bad names                    |
| Health checks         | TCP/HTTP/HTTPS probes that gate records          |

Each zone's detail view has its own tabbed layout: Records,
Activity, DNSSEC, Preview, Views (split-horizon), Health-checks.

## Zone management

### Creating a fabric apex zone

The apex zone is the per-fabric "top" — it holds the NS
delegations to every site zone and (if signed) the DS records
that anchor the trust chain.

1. `IPAM → DNS → Zones → New zone`.
2. **Type**: Primary apex.
3. **Name**: the fully-qualified apex (`prod.dcim.mil`,
   `internal.example.test`, etc.).
4. **Fabric**: the fabric this apex belongs to.
5. Leave SOA defaults as-is unless you need shorter timers; the
   defaults are tuned for push/pull rather than AXFR.
6. Save. The renderer will pick it up on the next collector
   poll.

You generally have **one apex per fabric**. If you need a second
apex (e.g. a tenant-scoped namespace inside the same fabric),
create it as another `apex`-kind zone — the renderer can carry
multiple.

### Creating a site zone

Site zones are mostly **auto-projected** from IPAM. When you
delegate from the apex (`site42.prod.dcim.mil` NS records pointing
at the site's auth pod), DCIM creates the corresponding site zone
the first time a record needs to land in it.

To create one explicitly:

1. `IPAM → DNS → Zones → New zone`.
2. **Type**: Primary site.
3. **Name**: the fully-qualified site name
   (`site42.prod.dcim.mil`).
4. **Fabric** + **Site**: both required for site zones.
5. Save.

Reverse zones (`<subnet>.in-addr.arpa` / `ip6.arpa`) are
**always** auto-projected from `Subnet` rows in IPAM — don't
create them by hand. They appear in the Zones list once IPAM has
subnets in this fabric.

### Adding manual records

Inside a zone's detail view, the **Records** tab has a `New
record` button. Type-specific forms cover:

- **CNAME** — single target FQDN. The form rejects targets
  pointing at the same name (no loops).
- **MX** — priority + target. The renderer emits in priority
  order.
- **TXT** — raw text, quoted on the wire. Multi-line + special
  chars supported via the quoting logic the renderer handles for
  you.
- **SRV** — priority, weight, port, target. All four required.
- **NS** — sub-delegation only. Don't use this to point a
  fabric apex at itself; the apex's own NS records are
  auto-emitted from `DnsServer` rows.
- **CAA** — flags, tag (`issue` / `issuewild` / `iodef`), value.

**A / AAAA / PTR** records *can* be created manually but the
normal path is to set `dns_name` on the `IPAddress` row in IPAM
and let the projector fill in A/AAAA/PTR for free. Records the
projector created carry a **"from IPAM"** badge and are read-only
in the UI; flip them to manual only if you need a hand-managed
override.

### IPAM-projected records

The worker cron `dns_sync_from_ipam` runs every 5 minutes (and on
explicit refresh from the zone detail page). It walks each
fabric's subnets, projects `IPAddress.dns_name` into A/AAAA + the
matching PTR in the reverse zone, and replaces only `source=ipam`
rows. Manual records survive across cycles untouched.

To preview what would change without committing, hit `POST
/api/v1/dns/zones/{id}/sync-from-ipam?dry_run=true`.

### DDNS records (from DHCP leases)

When the central DHCP service writes leases with hostnames into
DCIM, those records get `source=ddns` and are managed by the
lease lifecycle (created on lease, removed on release). Flip
`DCIM_DNS_DDNS_ENABLED=false` if your DHCP fleet is noisy and
you'd rather not have churn in zone files.

### Importing existing BIND zones

`Zones → <zone> → Import`. Accepts a `.zone` file in BIND format;
records the importer creates land as `source=manual`. DNSSEC
records (RRSIG, NSEC, NSEC3*, DS, CDNSKEY, CDS, TLSA, SSHFP) are
**skipped** by the importer — DCIM owns those via its own DNSSEC
flow.

### Freezing a zone for a maintenance window

`Zones → <zone> → Freeze`. A frozen zone refuses every mutating
endpoint with `422 zone is frozen` — record CRUD, BIND import, IPAM
sync, DNSSEC enable/disable/rotate, NSEC3 toggle, key delete, and
zone PATCH/DELETE. The freeze state itself is the only thing the
operator can still touch.

The detail page surfaces a Cloudscape warning banner whenever a
zone is frozen and a `frozen` badge next to the zone name; the
Create / Import / bulk-Delete buttons disable while the lock is on
so operators see the state instead of a wall of 422 toasts. Read
paths (preview, ds-records, listing keys) are unaffected.

Workflow:

1. Before starting a planned change, click **Freeze** on the
   zone. Confirm the dialog — the IPAM projector + worker cron
   jobs will start failing with 422 on this zone, which is
   intentional.
2. Do the out-of-band work the maintenance window is for (e.g.
   coordinated DNS cutover, ad-hoc TLD migration).
3. Click **Unfreeze**. Both transitions land in `audit_log`
   under `dns_zone.freeze` / `dns_zone.unfreeze`.

Both `POST /freeze` and `POST /unfreeze` are idempotent — a UI
that refreshes from stale state and re-posts the same action gets
the zone back unchanged. The `frozen` field is **not** writable
via the generic `PATCH /dns/zones/{id}` payload; every state
change goes through the named endpoints so the audit row always
exists. Capability: `dns:zones:update` for both.

## DNSSEC

### Enabling DNSSEC on a zone

1. Open the zone detail view → **DNSSEC** tab.
2. Click **Enable DNSSEC**. A confirmation prompt names the
   algorithm (default ECDSAP256SHA256, configurable system-wide by
   admin).
3. DCIM generates a KSK + ZSK, marks the zone `signed=true`, and
   the renderer starts emitting the `dnssec { key file … }` block
   on the next collector poll.
4. Copy the **DS records** from the DSSEC tab and give them to
   the parent zone's operator (your registrar, your TLD's DNSSEC
   contact, your enterprise parent zone team). Most operators
   ship two DS records — one SHA-1, one SHA-256 — DCIM emits
   SHA-256 (digest type 2) per RFC 6840 §5.2.
5. Once the parent's DS shows up in `dig DS <zone>`, your zone
   is part of the validation chain. Verify with `dig +dnssec @8.8.8.8
   <zone>` and look for the `ad` flag in the response.

### Switching to NSEC3

The default DNSSEC profile uses NSEC — efficient, but it lets
chain-walking validators enumerate your zone contents. NSEC3
hashes owner names so the chain isn't directly walkable. The
**Denial of existence** section on the DNSSEC tab lets you flip:

1. Confirm the auth pod is running the
   `ghcr.io/192d-wing/coredns-nsec3sign:v1.14.2-N` image. NSEC3
   responses from the stock `coredns/coredns` image won't work
   — there's no NSEC3 support in the upstream `dnssec` plugin.
2. Set **Salt** to `""` (empty) — RFC 9276 §3.1 recommends this
   for new deployments. Non-empty salts are accepted for
   compatibility with legacy NSEC3 chains being migrated in
   (must be hex, max 32 bytes).
3. Set **Iterations** to `0` — also RFC 9276 recommended. The UI
   accepts up to 150 (the historic BIND cap) for legacy
   compatibility.
4. **Opt-out** elides insecure delegations (NS-without-DS) from
   the chain — useful only on delegation-heavy zones (apex zones
   that delegate to many unsigned child zones). Leave off for
   site zones.
5. Click **Enable NSEC3**. The renderer flips the per-zone block
   from `dnssec` to `nsec3sign` on the next bundle poll.

To revert: click **Revert to NSEC** on the same section. Trust
chain stays intact; only the denial-of-existence profile
changes.

### Key rotation

ZSK rotation can be automatic or manual:

- **Automatic**: the DNSSEC tab has a **Rotation policy** button.
  Set days > 0 and the worker cron `dns_rotate_zsks` rotates the
  ZSK on that cadence, retiring the previous one.
- **Manual**: click **Rotate ZSK** on the DNSSEC tab header. New
  ZSK becomes active immediately, the previous one is marked
  retired. Retired keys stay in the DNSKEY RRset until you delete
  them (recommended: wait at least the SOA expire window so
  cached validators don't break).

**KSK rotation is always manual** because it requires uploading a
new DS to the parent. The UI flow:

1. Click **Rotate KSK** on the DNSSEC tab header.
2. Confirm the prompt — it warns that you must upload the new DS
   to the parent before the old DS can be retired.
3. DCIM generates the new KSK, marks the old one retired but
   keeps it in the DNSKEY RRset.
4. Copy the new DS records from the DS records section and ship
   them to the parent operator.
5. Wait for the parent to publish (a day to a week depending on
   the registrar's cadence).
6. Once `dig DS <zone>` shows only the new DS, **purge the
   retired KSK** by clicking the trash icon on its row in the
   Keys table. Don't purge sooner — caching validators verifying
   against the old DS will fail.

### Unsigning a zone

Hit **Unsign zone** on the DNSSEC tab. Type the zone name in the
confirmation modal (typed-confirmation to prevent fat-finger).
Withdraw the DS from the parent *first* — unsigning while a DS
still points at the zone produces sustained validation failures
for the duration of the negative-cache TTL.

## DNS server registration

Each site runs two CoreDNS pods: one **auth** (per-site
authoritative), one **recursive** (forwards to the local auth +
operator upstreams via anycast). Both need a `DnsServer` row in
DCIM:

1. `IPAM → DNS → Servers → New server`.
2. **Role**: `auth` or `recursive`.
3. **Site**: the site this pod lives at.
4. **Unicast IP**: the management IP the pod listens on
   (`172.30.42.10` and `172.30.42.20` in the example compose).
5. For `recursive` pods, scroll to **Anycast announce**:
   - **Anycast group**: pick the fabric's group; this drives the
     `/32` GoBGP announces.
   - **Announced to peer**: multi-select the BGP peers
     announcement targets. Usually the leaf switch + a redundancy
     pair.
6. Save.

The next collector poll renders the Corefile + (for recursives)
the GoBGP YAML, and the on-site stack picks them up
automatically. The Servers list shows **Last render** status
per server; a red badge there is your first signal of trouble.

### BGP peers + anycast groups

Both have their own management screens reachable from the Servers
panel. BGP peer creation flow:

1. `Servers → BGP peers → New peer`.
2. **Site**: where the peer is.
3. **Local AS / Peer AS / Peer IP**: standard BGP knobs.
4. **MD5 password** (optional but recommended) or **TCP-AO
   keychain** reference.
5. Save and bind to one or more `recursive` DnsServer rows from
   the Servers panel.

Anycast group creation:

1. `Servers → Anycast groups → New group`.
2. **Service**: `dns_recursive`.
3. **Anycast IPv4** (required) and **IPv6** (optional).
4. **Fabric**: which fabric this group belongs to.
5. Save.

## Health-checked records

Records can be gated by a TCP, HTTP, or HTTPS probe. When the
probe fails, the renderer drops the record from the rendered zone
file until it recovers. Useful for failover patterns (point a
single CNAME at two A records, gate each on its own probe).

1. `Zones → <zone> → Health-checks → New check`.
2. **Protocol** + **target IP** + **port** + **interval/timeout**.
3. Save. The probe starts immediately from the worker (or from a
   collector when site-local probing is configured).
4. On the record edit form, pick the check from the **Health
   check** dropdown.

The Health-checks tab shows live status; the per-zone Activity
tab logs probe transitions.

## Split-horizon views

Different answers for different client subnets. Useful for
internal/external value splits or fabric-scoped overrides.

1. `Zones → <zone> → Views → New view`.
2. **Name**: short label (`internal`, `dmz`, `default`).
3. **Match CIDRs**: client source IPs that select this view.
4. **Priority**: lower = higher precedence in the Corefile
   `view` plugin's first-match-wins order.
5. Save, then on each record set the view from the dropdown. A
   record with no view goes into the **default** view (clients
   that match no other view's CIDR list).

## Forwarders

Per-fabric stub-zone forwards + catch-all upstreams. Lives at
`Servers → Forwarders`.

- **Conditional forwarder** (specific zone name → specific
  upstream): for "send queries for `corp.example.com` to
  10.0.0.53" patterns.
- **Catch-all upstream** (fabric-wide): the `forward .` block in
  the recursive Corefile. Resolved through a three-level
  fallback chain at render time: fabric override (IPAM → Fabric
  edit → "DNS recursive upstreams" textarea), then system-wide
  override (see below), then the env-backed
  `DCIM_DNS_RECURSIVE_UPSTREAMS` default. First non-empty layer
  wins.

### System-wide upstream override

`Admin → System DNS`. Promotes `dns_recursive_upstreams` out of
the env into an editable `system_settings` row so operators on an
internal recursive estate (e.g. Active Directory DNS) can swap the
fleet-wide default without redeploying the API. The page shows
the current effective value, whether an override is active, and
the env-backed default for comparison.

- **Empty textarea + Save** clears the override; the renderer
  falls back to the env default.
- **Reset to default** is the same operation, surfaced as a
  button when the override is active.
- Per-fabric overrides still win; this only changes what
  fabrics without their own override use.

Capability: `admin:system-settings:read` to view,
`admin:system-settings:update` to edit. EnterpriseAdmin's `*`
covers both. The PUT endpoint normalizes input (strip, dedupe,
first-occurrence-wins) so reordering on the form reflects in the
rendered Corefile order.

## Blocklists (RPZ-lite)

`Servers → Blocklists → New blocklist`. Operate on the recursive
pod's response path:

- **Pattern**: literal name or `*.suffix` wildcard.
- **Action**: `nxdomain` (deny with NXDOMAIN), `nodata` (NODATA),
  `sinkhole` (rewrite to a sinkhole IP).

Bulk import is supported — paste a list of patterns into the
**Bulk add** modal.

## Verifying a signed zone end-to-end

Before pointing production traffic at a freshly-signed zone, run
the [comprehensive-test
harness](../../infra/coredns-nsec3sign/examples/comprehensive-test/)
against your auth pod. It boots the deployed image against a
hand-rolled zone exercising every code path the plugin handles —
positive answers, NXDOMAIN, NODATA, wildcard expansion, empty
non-terminals (deep hierarchies), and secure / insecure
delegation referrals — and produces `dig +dnssec` output an
operator can read in 30 seconds.

Use it as the sanity check after:

- **Enabling NSEC3** on a zone for the first time. Confirms the
  custom image is actually running and the signing path works on
  the wire.
- **Bumping the plugin image** to a new tag. Confirms the new
  build still produces validating responses across all the
  shapes a real zone might hit.
- **Rotating a key** (KSK or ZSK). The harness's RRSIG inception
  field shows whether the new key is the one signing.

The harness uses its own throwaway zone (`example.test.`) so it
doesn't touch production data. Walk-through is in the
[harness README](../../infra/coredns-nsec3sign/examples/comprehensive-test/README.md).

## Common troubleshooting

### "My zone changes aren't propagating"

- Open `Servers`. The relevant `auth` row should show
  `last_render_status = ok` and `last_render_at` < 1 minute.
- If render is older than a couple minutes, the collector is
  stuck or unreachable. Look at the collector's logs (the
  Activity tab on the site shows recent events).
- If render is recent but the change isn't visible:
  - For a new record: confirm it's saved (refresh the records
    list) and check it doesn't have a health check that's
    failing.
  - For a DNSSEC change: signature cache may be serving stale
    sigs. Wait ~30 seconds for cache expiry or restart the auth
    pod.

### "I can't enable NSEC3 — the button is disabled"

The zone must be signed first. Enable DNSSEC, then come back and
flip NSEC3.

### "DNSSEC validators reject my responses"

Most common causes, in order:

1. **DS at parent doesn't match KSK** — copy the DS records from
   the DNSSEC tab again, compare to `dig DS <zone>`. Re-upload
   to parent if needed.
2. **NSEC3 enabled on a stock CoreDNS image** — admin needs to
   bump the auth pod to the custom `coredns-nsec3sign` image.
3. **Old NSEC3 cached** — wait one negative-cache TTL (default 5
   minutes from the SOA Minttl) for downstream validators to
   refetch.

### "BGP session won't come up"

- Confirm the peer's local + remote AS match what's in the
  `BgpPeer` row.
- MD5/TCP-AO secrets are case-sensitive. The leaf-side and
  DCIM-side must match exactly.
- Look at `gobgp` logs in the site stack: `docker compose -p
  siteN logs gobgp`.

### "Anycast IP works from some clients but not others"

Routing issue, not DNS. The leaf is announcing the route to only
a subset of clients. Talk to whoever owns the underlay network.

## DNS overview dashboard

The top-level **DNS** entry in the side-nav (`/dns`) is the single
page operators land on for an at-a-glance view of the whole fabric.
Everything on the page refetches every 30 seconds.

### Reading it

| Panel | What it shows | Where the number comes from |
| --- | --- | --- |
| **Global** strip (5 KPIs) | Current QPS, NXDOMAIN%, p95 latency, zones, servers | Last sample per server (QPS), window aggregates (rates), live row counts (zones/servers) |
| **QPS timeline** | Two-axis line chart: QPS (left) + p95 ms (right) over the selected window | Bucketed deltas from `dns_server_metrics_samples` |
| **By site** table | Per-site rollup, sorted by current QPS | Same window samples, grouped by `DnsServer.site_id` |
| **Top queried names** | Top-N name + type with counts; copy-to-clipboard icon per row | `top_names` JSONB column from dnstap-shipping CoreDNS auth pods |
| **Server health** | Each `DnsServer` with role, engine, last-render status chip | `DnsServer.last_render_*` columns updated by the collector |

The header strip has two controls:

- **Window** — segmented 1h/6h/24h. Changes the `minutes` query
  param the page polls, re-buckets every aggregate.
- **Fabric scope** — the existing top-nav fabric picker. When set,
  every aggregate on the page narrows to that fabric (servers,
  zones, samples, anycast groups). Header description reads
  `fabric: <name>` so the scope is always visible.

A storage footer in the header description (`top_names: N rows ·
XB avg · YMB total`) lets you eyeball the JSONB column's disk
footprint against `dns_metrics_retention_days` (default 14).

### Wiring per-query name capture (dnstap)

The Top queried names card stays in its "Coming soon" placeholder
state until **both** legs of the dnstap path are connected:

1. **Central** emits the directive into CoreDNS auth Corefiles —
   on by default via `DCIM_DNS_DNSTAP_ENABLED=true` (set in the
   central docker-compose api env). Renders a
   `dnstap <socket-path> full` line in every zone block.
2. **Collector** listens on that socket — set `dnstap_socket` on
   the auth entry in `collector.yaml`:

   ```yaml
   dns:
     servers:
       - id: <auth dns_server_id>
         role: auth
         output_dir: /var/lib/dcim-dns/auth
         dnstap_socket: /var/lib/dcim-dns/auth/dnstap.sock
   ```

   Restart the collector. Successful start logs
   `dnstap_loop_start` and `dnstap_server_start`.

Hickory recursive pods can't supply top-names — Hickory has no
dnstap support upstream. The dashboard says so explicitly when
`top_names` is `null` (vs an empty list, which means "dnstap is
wired but the window saw zero queries").

### Troubleshooting

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| Dashboard shows 0 QPS on a server with traffic | Collector's prom scrape failing | `docker compose -p siteN logs collector \| grep dns_metrics_cycle_failed` — typical causes: `metrics_url` wrong, Hickory binary built without `prometheus-metrics` |
| Hickory recursive: `qps_now: null` | Scrape disabled or Hickory upstream image lacking metrics | Confirm `metrics_enabled: true` on the recursive in `collector.yaml`. If using the upstream `hickorydns/hickory-dns` image, swap to `ghcr.io/192d-wing/hickory-prom:v0.26.0-1` (see `infra/hickory-prom/`) |
| Top queried names says "No per-name data yet" | dnstap not wired on any server | Confirm `DCIM_DNS_DNSTAP_ENABLED=true` on the api container AND `dnstap_socket` set on at least one auth entry in `collector.yaml` |
| Top queried names is empty list | dnstap wired, no traffic in window | Generate test traffic: `dig @<auth-ip> -p 53 <zone> SOA` |
| `resolver_reloaded=false` on Hickory bundle apply | Hickory's pidfile missing | Already fixed in collector via `/proc/<pid>/comm` fallback; if still failing, confirm site stack runs `pid: host` on both collector + recursive containers |
| Latency p95 is null but QPS > 0 | Histogram has too few samples or Hickory's `cache_miss_*` histogram is empty | Drive real load through the recursive (cache-hit latency is sub-ms and is intentionally excluded); for CoreDNS auth, the duration histogram needs at least 5 samples in the window |
| Dashboard scoped to fabric shows zero everything | Fabric has no DnsServers | Add at least one `DnsServer` row bound to the fabric, or switch the fabric picker back to "all fabrics" |

### Cutover playbooks

**Enable Hickory recursive on a fabric:**

```bash
curl -X PATCH /api/v1/ipam/fabrics/$FABRIC_ID \
  -H "content-type: application/json" \
  -d '{"recursive_engine":"hickory"}'

# Then layer the Hickory overlay on the site stack:
docker compose -p siteN \
  -f infra/docker/site-dns/docker-compose.yml \
  -f infra/docker/site-dns/docker-compose.hickory.yml \
  up -d --force-recreate coredns-recursive
```

**Enable NSEC3 on a signed zone:**

```bash
# Zone must be signed first (POST .../enable-dnssec).
curl -X POST /api/v1/dns/zones/$ZONE_ID/nsec3 \
  -H "content-type: application/json" \
  -d '{"salt":"ABCD","iterations":0,"opt_out":false}'
```

Auth pod must be running the custom `coredns-nsec3sign` image —
the upstream CoreDNS rejects the `nsec3sign` directive and falls
back to its previous config.

**Roll back:**

- Hickory → CoreDNS recursive: PATCH the fabric back to
  `recursive_engine:"coredns"` and re-run with the base compose
  file (no `-f docker-compose.hickory.yml`).
- NSEC3 → NSEC: `DELETE /api/v1/dns/zones/$ZONE_ID/nsec3`. The
  renderer switches back to the upstream `dnssec` plugin block.

## DoH / DoT on the recursive

Encrypted DNS to clients (DNS-over-HTTPS, RFC 8484; DNS-over-TLS,
RFC 7858). Both ride the same TLS cert and listen on the recursive
Hickory pod alongside the existing plain DNS on :53.

**Image:** requires the custom `hickory-prom:v0.26.0-2`+ image built
with `tls-ring` + `https-ring` Cargo features (the v0.26.0-1 image
shipped before this feature lands rejects `tls_listen_port` on
parse). Pull `ghcr.io/192d-wing/hickory-prom:latest` or rebuild
from `infra/hickory-prom/` if you maintain your own registry.

**Cert format:** Hickory wants the certificate chain + private key
concatenated into a single PEM file. Mount it into the recursive
container at a known path (e.g. `/etc/dcim-dns/tls.pem`).

**Enable:** set these on the central api environment:

```bash
DCIM_DNS_HICKORY_DOH_ENABLED=true
DCIM_DNS_HICKORY_DOT_ENABLED=true
DCIM_DNS_HICKORY_TLS_CERT_PATH=/etc/dcim-dns/tls.pem
# defaults below — override only if you need non-standard ports
DCIM_DNS_HICKORY_TLS_LISTEN_PORT=853
DCIM_DNS_HICKORY_HTTPS_LISTEN_PORT=443
DCIM_DNS_HICKORY_DOH_PATH=/dns-query
```

The next bundle poll renders a `[tls_cert]` block + the listener
ports into the recursive's config.toml; Hickory reloads on SIGHUP
and starts answering on 853/443. Plain DNS on :53 stays on
regardless — operators flip individual clients to DoT/DoH on their
own schedule.

**Test the listeners:**

```bash
# DoT — kdig from `knot-dnsutils` is the canonical client
kdig +tls @<recursive-ip> -p 853 example.com

# DoH — curl with the dns-message MIME type
curl --resolve dns.example.com:443:<recursive-ip> \
  -H 'accept: application/dns-message' \
  "https://dns.example.com:443/dns-query?dns=$(printf 'example.com' | base64url-encode)"
```

If the cert's SAN doesn't include the address clients hit, they'll
trip on hostname verification — use a real DNS name in the cert and
have clients resolve it (typically via the same recursive 😅).

## Recursive client access control

Hickory 0.26 exposes `allow_networks` / `deny_networks` config
fields and DCIM wires them through as **per-fabric** CIDR ACLs on
the `Fabric` row (`dns_deny_networks` + `dns_allow_networks`). The
recursive's rendered TOML carries the merged list:

```toml
deny_networks  = ["10.99.0.0/24"]
allow_networks = ["10.0.0.0/8", "192.168.0.0/16"]
```

**Caveat (upstream Hickory):** during pilot smoke testing on
v0.26.0 the renderer-side wiring was confirmed end-to-end, but the
binary did NOT actually reject queries from IPs outside the
allowlist — the access-control check appears to not be hooked up
for UDP in this release. Track upstream
[PR-2126](https://github.com/hickory-dns/hickory-dns/pull/2126);
the DCIM data path is ready for when Hickory ships a working
enforcement (or we patch our `hickory-prom` build to do so). Until
then, treat the ACL config as documenting intent — actual
enforcement needs one of the options below.

### Strict allowlist mode (opt-in)

When `DCIM_DNS_HICKORY_ALLOW_NETWORKS_STRICT=true`, the renderer
emits one extra top-level line whenever a fabric has a non-empty
`allow_networks` list:

```toml
deny_networks  = ["10.99.0.0/24"]
allow_networks = ["10.0.0.0/8"]
allow_networks_strict = true
```

This pairs with the upstream PR `access: add opt-in strict-
allowlist mode for allow_networks`. The patch only changes one
row in the access-control matrix — the **`allow` + `deny` both
set, source outside both** case. The other rows behave as stock
upstream:

| `allow` | `deny` | strict | Outcome for client IP X |
| --- | --- | --- | --- |
| empty | empty | any | accept (open recursive) |
| set | empty | any | refuse unless X ∈ allow |
| set | set | off | **accept if X ∉ both lists** (carve-out fall-through) |
| set | set | on | refuse unless X ∈ allow (firewall semantics) |
| any | X ∈ deny | any | refuse |

That third row is what the PR exists to fix. The default fall-
through "any source outside both lists is allowed as long as
some deny entry exists" surprises operators who reach for
`allow_networks` expecting firewall semantics — `allow=[10/8],
deny=[10.99/24]` reads as "only allow 10/8, additionally deny
10.99/24" but actually permits any source outside both lists.
Strict mode opts into the firewall interpretation.

The flag is **off by default** in the tracked compose file —
stock upstream Hickory builds reject the unknown field on parse,
so flipping it requires the patched
`hickory-prom:v0.26.0-strict-dev` image (or a future upstream
release that lands the PR).

**Verified end-to-end** (2026-05-12, hickory-prom:v0.26.0-strict-dev,
prod fabric on site42 dns-net 172.30.42.0/24):

```text
allow=[172.30.42.5/32], deny=[172.30.42.100/32]
  src=172.30.42.5   → NoError  (in allow)        — strict on/off
  src=172.30.42.99  → NoError  (carve-out)       — strict off
  src=172.30.42.99  → Refused  (firewall)        — strict ON
  src=172.30.42.100 → Refused  (in deny)         — strict on/off
```

To enable on a deployment running the patched image, override the
env var in `compose.override.yml`:

```yaml
services:
  api:
    environment:
      DCIM_DNS_HICKORY_ALLOW_NETWORKS_STRICT: "true"
```

The flag is also a no-op on deny-only fabrics — the renderer
suppresses emission when `allow_networks` is empty so unused
configs don't carry it as dead weight.

**Real QPS-based rate-limiting** is still not natively supported
on either engine. The roadmap item is parked pending either an
upstream PR to Hickory or one of the out-of-band approaches:

- **Host-side nftables/iptables `hashlimit`** on UDP/TCP 53. This
  is the standard production answer; not driven from DCIM but
  works regardless of resolver. Sample rule on the recursive host:

  ```bash
  nft add rule inet filter input udp dport 53 \
    meter ratelimit { ip saddr limit rate 100/second } accept
  ```

- **A dnsdist sidecar** in front of Hickory if you need real
  per-client QPS limits + selective blocking. Heaviest option but
  the most flexible.

## Pointers

- Admin / deployment: [admin-guide.md](admin-guide.md)
- Code internals: [implementation.md](implementation.md)
- Design context: [../design/dns-integration.md](../design/dns-integration.md)
- API reference: <http://localhost:8000/docs#tag/dns> (OpenAPI)
