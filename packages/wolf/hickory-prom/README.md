# hickory-prom — Hickory DNS with Prometheus metrics enabled

A pre-built `hickorydns/hickory-dns:0.26.0` clone that ships the
`prometheus-metrics` Cargo feature compiled in (the upstream image
does not). Plus `dnssec-ring` so a recursive that forwards to a
signed authoritative zone actually validates the response.

## Why this exists

Hickory's binary is built with default features only on the upstream
`hickorydns/hickory-dns:*` images. That excludes the prometheus
exporter — a stock Hickory recursive listens only on port 53 and
exposes no `/metrics` endpoint. The collector's scrape loop has
nothing to read, so QPS / error rate / latency are silently absent
from the DNS dashboard for any fabric on `recursive_engine=hickory`.

This image rebuilds the same `v0.26.0` source with
`--features "prometheus-metrics dnssec-ring"`. The runtime contract
is identical to the upstream image — same entrypoint
(`/hickory-dns`), same default ports, same TOML config schema — plus
a `/metrics` endpoint at whatever `prometheus_listen_addr` is set in
the rendered config.

## Building

```bash
make build                 # produces ghcr.io/192d-wing/hickory-prom:v0.26.0-1
make push                  # pushes :tag and :latest
HICKORY_VERSION=v0.26.1 REVISION=2 make build push  # re-pin upstream
```

The build clones Hickory at the pinned tag inside the Docker
context, so the only host-side requirement is Docker (no Rust
toolchain, no git checkout).

## Wiring into the site stack

Swap the `coredns-recursive` service's image in
`deploy/docker/site-dns/docker-compose.hickory.yml` from
`hickorydns/hickory-dns:latest` to
`ghcr.io/192d-wing/hickory-prom:v0.26.0-1`. Then add a
`prometheus_listen_addr` to the rendered Hickory config (the DCIM
renderer will emit this when it knows the recursive needs it; until
then, operators can append the line manually or just wait for the
Phase B renderer change).

## Building from a local checkout (testing upstream PRs)

`Dockerfile.local` builds Hickory from a local workspace checkout
instead of cloning the pinned upstream tag. Used today to pilot the
upstream strict-allowlist PR ahead of its merge; remove the
override and this Dockerfile once the PR lands and a real release
tag is cut.

```bash
# From the repo root. Expects a hickory-dns workspace checkout at
# the sibling path `../../../hickory-dns` (override with
# LOCAL_HICKORY_PATH=...).
make -C packages/wolf/hickory-prom local-build   # builds :v0.26.0-strict-dev
make -C packages/wolf/hickory-prom local-push    # builds + pushes to ghcr
```

The strict-dev tag is intentionally distinct from the release
tags (`v0.26.0-N`) so a stale local build can't be deployed as
"the release image" by mistake.

Operators on the patched image can then opt in by setting
`DCIM_DNS_HICKORY_ALLOW_NETWORKS_STRICT=true` in their compose env
— see `docs/dns/operator-guide.md` → Recursive client access
control → Strict allowlist mode for the four-case behavior table.
