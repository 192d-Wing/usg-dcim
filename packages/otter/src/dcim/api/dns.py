"""DNS HTTP routes are fully on otter-go (PR 33 cutover).

The schemas + ORM models still live under packages/otter/src/dcim/
{schemas,models}/dns.py — they back DB rows the surviving non-DNS
Python paths still read (alert evaluation loop, region-deploy
preflight, IPAM sync-from-DNS reverse helpers).

History:
- PR 33 cutover routes the whole /api/v1/dns/* prefix to otter-go.
  The bundle endpoint the collector pulls from (GET /servers/{id}/
  bundle) was the last gap; it ported over PRs #247-#255 (catalog
  zone renderer, auth Corefile, BIND key files + CDNSKEY/CDS, etag,
  recursive Corefile + blocklist templates, query primitives,
  bundle assembler + HTTP endpoint, catalog/DNSSEC/extras loaders,
  split-horizon views + catalog primaries).
- /api/v1/dns/bgp-peers/* moved earlier in PR #214.
- The rest of the surface (zones/records/keys/health-checks/anycast-
  bindings/blocklists/views/forwarders/catalog-zones/dashboard/sync-
  from-ipam/preview/import/freeze/unfreeze/enable-dnssec/disable-
  dnssec/rotate-key/nsec3) had Go handlers earlier (PRs 43-83 era)
  but stayed dark until PR 33 flipped ingress.

Recursive servers still return 501 from the bundle endpoint until
GoBGP YAML + RPZ zone + recursive engine helpers port in a
follow-up; the auth-bundle path is feature-complete.

This module is kept as a stub so any rare import-path reference in
tooling resolves to a no-op rather than ImportError. The smoke test
at packages/otter/tests/test_smoke.py negative-asserts that the
Python app never advertises /api/v1/dns/* routes.
"""
from __future__ import annotations
