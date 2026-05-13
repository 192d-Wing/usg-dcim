# DNS

DCIM-DNS makes the central inventory the source of truth for
internal DNS. Operators create zones and records via the UI/API;
the renderer produces a Corefile + zone files + GoBGP config on
every collector poll; site-local CoreDNS pods reload from a
shared volume.

Two pods per site:

- **`coredns-auth`** is authoritative for the per-site subdomain
  and loads the fabric-wide bundle for resilience.
- **`coredns-recursive`** forwards `*.<fabric_apex>` to the local
  auth and everything else to operator-configured upstreams.
  Reachable via a per-fabric anycast IP advertised by a GoBGP
  sidecar.

DNSSEC is supported with KSK + ZSK rotation, online RRSIG signing,
and either NSEC (upstream `dnssec` plugin) or NSEC3 (custom
`coredns-nsec3sign` plugin) for denial-of-existence.

## Guides

| You are…                                                          | Read this                              |
| ----------------------------------------------------------------- | -------------------------------------- |
| Deploying the central stack, managing secrets, bootstrapping sites | [admin-guide.md](admin-guide.md)       |
| Creating zones, managing records, enabling DNSSEC/NSEC3 from the UI | [operator-guide.md](operator-guide.md) |
| Modifying the code, adding a feature, debugging a regression      | [implementation.md](implementation.md) |
| Managing CDS / CDNSKEY auto-propagation for KSK rotation          | [cds-cdnskey.md](cds-cdnskey.md)       |

## Design context

The architecture decisions — push/pull instead of AXFR, two-pod
auth+recursive split, per-fabric anycast, the choice to defer
DNSSEC initially and add it in v2 — live in
[../design/dns-integration.md](../design/dns-integration.md).

## Custom CoreDNS plugin

The `coredns-nsec3sign` plugin lives under
[infra/coredns-nsec3sign/](../../infra/coredns-nsec3sign/) with
its own README and SECURITY-REVIEW.md. Upstream CoreDNS's
`dnssec` plugin only emits NSEC chains; the custom plugin fills
the NSEC3 gap (RFC 5155) and ships as a drop-in image at
`ghcr.io/192d-wing/coredns-nsec3sign:v1.14.2-N`.

## Other entry points

- Site-stack bring-up: [../../infra/docker/site-dns/README.md](../../infra/docker/site-dns/README.md)
- Live API reference (OpenAPI): <http://localhost:8000/docs#tag/dns>
- Roadmap: [../../ROADMAP.MD](../../ROADMAP.MD)
