# coredns-nsec3sign

A CoreDNS plugin that performs **on-the-fly NSEC3 signing** for
authoritative zones. It's the NSEC3 counterpart to CoreDNS's upstream
[`dnssec`](https://coredns.io/plugins/dnssec/) plugin, which only
emits NSEC chains and has no NSEC3 support.

This plugin lives inside the `usg-dcim` monorepo because DCIM is its
only consumer today; the Go module path
(`github.com/192d-wing/coredns-nsec3sign`) is structured so it can be
extracted to its own repo later without touching importers.

## Why a new plugin

The stock `dnssec` plugin walks the zone and signs RRsets on demand,
attaching NSEC records for authenticated denial of existence. That's
fine for most internal zones, but NSEC lets attackers walk the entire
zone (zone enumeration) by following the NSEC chain. NSEC3 hashes
owner names so the chain isn't directly walkable, at the cost of
slightly more computation per denial.

DCIM zones can carry sensitive hostnames (asset IDs, MAJCOM-scoped
names, mission tags), so NSEC3 is the right default for our
authoritative pods. The DCIM model already carries `nsec3_salt` and
`nsec3_iterations` columns on `DnsZone` — they were defined in the
DNSSEC plan but couldn't be honored because CoreDNS itself couldn't
emit NSEC3.

## Corefile syntax

```caddyfile
example.mil:53 {
    file /var/lib/dcim-dns/auth/example.mil.zone
    nsec3sign {
        key file /var/lib/dcim-dns/auth/keys/Kexample.mil.+013+12345
        zone file /var/lib/dcim-dns/auth/example.mil.zone
        salt ""           # empty salt (RFC 9276 recommended default)
        iterations 0      # 0 iterations (RFC 9276 recommended default)
        opt_out           # optional; skip NSEC3 for insecure delegations
        cache_capacity 10000
    }
}
```

The duplicate `zone file` path is intentional: the chain builder re-
parses the zone file via `miekg/dns.ZoneParser` rather than walking
the `file` plugin's private zone tree. Trades a small duplicate
parse cost (~10 ms per zone at startup, ~600 KB resident per
thousand owner names) for zero coupling to CoreDNS internals.
DCIM's renderer fills both lines from the same source so operators
only see this duplication in the rendered Corefile.

The key file format matches BIND's `Kname+alg+tag.{key,private}` pair
— the same format DCIM already renders via
[`render_dnssec_key_files`](../../backend/src/dcim/services/dns.py).
Switching a zone from NSEC to NSEC3 is a one-line Corefile change
(`dnssec` → `nsec3sign`) plus a salt/iterations decision.

## Build

The stock `coredns/coredns:1.11.3` image can't load external plugins
— CoreDNS plugins are compiled in, not loaded at runtime. The
`Dockerfile` here produces a drop-in replacement image:

```bash
make build               # local image: ghcr.io/192d-wing/coredns-nsec3sign:v1.11.3-1
make push                # push to ghcr (requires docker login)
```

The build:

1. Clones CoreDNS at the pinned version (`COREDNS_VERSION` arg).
2. Splices `nsec3sign:github.com/192d-wing/coredns-nsec3sign/nsec3sign`
   into `plugin.cfg` immediately after the `dnssec` line so the two
   siblings sit in the same response-chain slot.
3. Adds a `replace` directive pointing CoreDNS at our local copy
   inside the build context, so the build never reaches out for a
   published version of this module.
4. Runs `go generate` (regenerates `zplugin.go` based on the patched
   `plugin.cfg`) and `go build`.

The resulting binary is distroless-packaged and ships in under 50 MB.

## Site stack wiring

[`infra/docker/site-dns/docker-compose.yml`](../docker/site-dns/docker-compose.yml)
points the `coredns-auth` service at `coredns/coredns:1.11.3` today.
Once an image is published, that line flips to
`ghcr.io/192d-wing/coredns-nsec3sign:v1.11.3-1` and the plugin is
available wherever auth pods run. The `coredns-recursive` pod doesn't
need the custom image — only authoritative zones sign.

## Status

Build-out plan, with the current cursor on step 7:

| Step | What                                                         | Status   |
| ---- | ------------------------------------------------------------ | -------- |
| 1    | Module scaffolding + custom CoreDNS image + noop plugin      | done     |
| 2    | BIND key loader (`Kname+alg+tag.{key,private}` reader)       | done     |
| 3    | NSEC3 chain builder (one-shot, sorted by hash)               | done     |
| 4    | Positive-response RRSIG signing                              | done     |
| 5    | Denial-proof algorithm (NXDOMAIN closest-encloser, NODATA)   | done     |
| 5b   | Zone-file ingestion — chain populated from BIND zone at boot | done     |
| 6    | Signature cache + Prometheus metrics                         | done     |
| 7    | DCIM renderer change — emit `nsec3sign` for NSEC3 zones      | **next** |
| 8    | End-to-end smoke against `site-dns/docker-compose.yml`       | todo     |

As of step 5b the plugin is functionally complete for authoritative
DCIM zones: positive responses ship signed; NXDOMAIN and NODATA
ship with the NSEC3 closest-encloser / matching proofs validators
need; the chain is populated at startup from the same BIND zone
file the `file` plugin reads. The remaining build-out steps target
production hardening (signature cache, reload, metrics, DCIM-side
renderer change, end-to-end smoke against the site stack).

## Layout

```text
infra/coredns-nsec3sign/
├── README.md          (this file)
├── go.mod             github.com/192d-wing/coredns-nsec3sign
├── Dockerfile         multi-stage custom CoreDNS build
├── Makefile           build / push / test targets
└── nsec3sign/
    ├── setup.go       Corefile parser + plugin registration
    ├── nsec3sign.go   plugin handler — ServeDNS interceptor
    ├── keys.go        BIND-format DNSSEC key pair loader
    ├── zone.go        BIND-format zone file → chain ingestion
    ├── chain.go       sorted NSEC3 hash chain + lookup primitives
    ├── signer.go      RRSIG generation over RRsets
    ├── sigcache.go    LRU signature cache + expiry janitor
    ├── denial.go      NXDOMAIN / NODATA proof construction
    └── metrics.go     Prometheus counters / gauges
```

## Metrics

Mirrors the upstream `dnssec` plugin's metric names so dashboards
written for `coredns_dnssec_*` translate with only a name rewrite.
All counters carry a `server` label set from
`metrics.WithServer(ctx)`.

| Metric                                 | Type    | Labels           |
| -------------------------------------- | ------- | ---------------- |
| `coredns_nsec3sign_cache_hits_total`   | counter | `server`         |
| `coredns_nsec3sign_cache_misses_total` | counter | `server`         |
| `coredns_nsec3sign_cache_entries`      | gauge   | `server`, `type` |
| `coredns_nsec3sign_denials_total`      | counter | `server`, `type` |
| `coredns_nsec3sign_chain_entries`      | gauge   | `zone`           |

The `denials_total{type}` label is `nxdomain` or `nodata`; the
`cache_entries{type}` label is `signature` (room for future cache
types).

## References

- [RFC 5155 — DNSSEC Hashed Authenticated Denial of Existence](https://datatracker.ietf.org/doc/html/rfc5155)
- [RFC 9276 — NSEC3 Parameter Settings Guidance](https://datatracker.ietf.org/doc/html/rfc9276)
- [CoreDNS plugin developer guide](https://coredns.io/manual/plugins/)
- [Upstream `dnssec` plugin source](https://github.com/coredns/coredns/tree/master/plugin/dnssec)
  — the closest existing reference for what this plugin needs to do.
