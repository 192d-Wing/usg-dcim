# Python collector — DEPRECATED

This package is retained for **reference and emergency rollback only**.
The active collector implementation is [services/go-collector](../services/go-collector),
which reached parity in Phase 4 (commit `ddcde13`).

## Why we cut over

- Static binary, ~17 MB, idle RSS ~7 MB vs. the Python collector's ~90 MB.
- No `pip`, no venv, no aging `pysnmp-lextudio` / `pymodbus` dep chain at site edges.
- Native gRPC story with the existing Go neighbors (gobgpd, CoreDNS).

## Wire-shape parity

The Go collector is engineered to produce **byte-identical telemetry**:

| field | parity |
|---|---|
| metric names | same (`thermal.<sensor>.tempC`, `power.consumed.W`, `ipmi.<name>`, …) |
| tags | same shape (`oid`, `host`, `sensor`, `address`, `source`) |
| dnstap top-K | same `{name, type, count}` payload, same `_TOP_NAMES_SHIP_K = 100` cap |
| DNS bundle apply | same atomic-write + sync-dir + reload-signal sequence |
| GoBGP RIB reconcile | same gobgp-CLI diff + `_gobgp_target_args` form |

A site mid-cutover with one collector on each implementation reads as a
single set on the central dashboards.

## Rolling back

Edit `infra/docker/docker-compose.yml`, flip the `collector` service's
`build.context` back to `../../collector` and re-add the
`environment: { PYTHONUNBUFFERED: "1" }` block. Rebuild:

```sh
docker compose --profile collector up -d --build --no-deps collector
```

The collector config YAML is byte-compatible between the two binaries
(the Go loader was built around `collector/src/dcim_collector/config.py`),
so no config changes are required either direction.

## What's NOT in the Go port (yet)

Two paths the Python implementation has that the Go one doesn't:

- **SNMPv3.** The Go SNMP driver supports v1 and v2c; v3 needs the
  auth/priv key plumbing that's a follow-up phase.
- **Native GoBGP gRPC.** Phase 4 ships a `gobgp` CLI shellout reconciler
  (parity with the Python). A future phase can swap in the
  `osrg/gobgp/v3/api` Go gRPC client.

If you depend on either, do not cut a site to the Go collector yet —
keep its `collector` service pointing at this directory until those
phases land.

## Code retention

This package will stay in tree at least until:

1. Every active site has been cut to the Go collector and observed for a
   full week without metric-name regressions or missing samples.
2. SNMPv3 and native GoBGP gRPC have landed in the Go collector.

After that, this directory can be removed in a single commit. Until
then, **do not** delete it — `infra/docker/docker-compose.yml`'s
rollback comment depends on the source being here.
