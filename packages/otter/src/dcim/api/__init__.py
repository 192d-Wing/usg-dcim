"""Versioned API surface — v1."""

from fastapi import APIRouter

from .regiondeploy import router as regiondeploy_router

router = APIRouter()
# /api/v1/auth/* moved to otter-go (PR 179). The umbrella chart
# routes /api/v1/auth → otter-go; Python no longer registers the
# router so a misrouted internal call fails loud instead of silently
# double-serving login/me/tokens. The schemas + models for User /
# ApiToken / Role still live under packages/otter/src/dcim/{schemas,
# models}/auth.py — they back DB rows other Python paths still read.
#
# /api/v1/inventory/* fully moved to otter-go (this PR). Cables PATCH
# was the last gap; with it ported, the longer-prefix
# /api/v1/inventory/cables → otter ingress rule was collapsed.
# /api/v1/collectors/* fully moved to otter-go (PR 21). Go now has
# parity on the enroll + heartbeat write paths (the previously
# deferred crypto + audit wiring) alongside list/get/config/enabled/
# decommission. Token shape: "enroll_" + token_urlsafe(32);
# sha256-hashed and stored in collectors.enrollment_token_hash.
# /api/v1/ingest/telemetry JSON fallback moved to otter-go (PR 22).
# heron already owned the high-throughput mTLS path; the Python
# JSON-fallback handler is gone. The Go handler emits no Prometheus
# metrics — heron is canonical for dcim_telemetry_* counters, matching
# the cutover policy in metrics.py L26-L38 (_GO_PORTED_DISABLED).
# /api/v1/telemetry/series moved to otter-go (PR 178). The umbrella
# chart routes /api/v1/telemetry → otter-go; Python no longer registers
# the router so a misrouted internal call fails loud instead of
# silently double-serving.
# /api/v1/alerts/* fully moved to otter-go (this PR). The arq-driven
# evaluation loop in services/alerts.py is untouched — only the HTTP
# routes move. The 12 Go handlers were implemented in PR 45 but
# stayed dark until the ingress cutover landed here.
# /api/v1/dashboards/* fully moved to otter-go across phases 1-3.
# enterprise + free-space + sites/at-risk + assets/{id} + sites/{id}
# + racks/{id} + 3 forecast endpoints all on otter-go now;
# services/{capacity,power_chain,forecast}.py retired alongside.
# /api/v1/search moved to otter-go (PR-search). The umbrella chart
# routes /api/v1/search → otter-go; Python no longer registers the
# router so a misrouted internal call fails loud instead of silently
# double-serving.
# /api/v1/stencils/* fully moved to otter-go (PR 19). The Go
# implementation in internal/stencils serves the static catalog;
# Python no longer registers the router so a misrouted internal
# call fails loud instead of silently double-serving.
# /api/v1/power/* fully moved to otter-go (PR 20). Go now matches
# Python byte-for-byte on capability codes (power:outlets:*), audit
# event shape (action="power.connect"/"power.disconnect",
# target_type="outlet", metadata={asset_id,...}), and the friendly
# 409 "already connected; disconnect it first" message that the UI
# surfaces. The PDU outlet listing + connect/disconnect handlers all
# move together so finch's power-chain panel keeps working as-is.
# /api/v1/audit/* moved to otter-go (PR 180). The umbrella chart
# routes /api/v1/audit → otter-go; Python no longer registers the
# router so a misrouted internal call fails loud instead of silently
# double-serving. The audit_log table + the security.audit.record()
# helper still live in Python (mutation handlers write to it).
#
# /api/v1/admin/* fully moved to otter-go — including the
# capabilities/catalog + system/dns-settings routes that briefly
# lingered behind the ingress split in PR #182. The longer-prefix
# ingress paths that kept those on Python are gone.
# /api/v1/notifications/* fully moved to otter-go (this PR). The
# existing channels CRUD was on Go (PR 45 + RBAC fix in PR #207); the
# new POST /channels/{id}/test endpoint mirrors Python's
# services.notifications dispatch (webhook + slack + email) so
# operators can verify wiring. Python no longer registers the router
# so a misrouted internal call fails loud instead of silently
# double-serving. services/notifications.py + models/notifications.py
# still exist — the alert evaluation loop in Python (untouched) calls
# notif_svc.dispatch() directly during fire/resolve events. Channel
# rows in Postgres remain the canonical config source for both.
# /api/v1/organizations/* fully moved to otter-go (PR 19). Go's
# internal/organization has parity (CRUD). The ORM model still
# exists for cross-module FK reads.
# /api/v1/ipam/* fully moved to otter-go (PR 18). PR 42 ported the
# basic CRUD; PR 54 + 55 added 1-hop and 2+ hop ABAC; PR 17 brought
# DHCP under the same Prefix rule. Python no longer registers the
# router so a misrouted internal call fails loud instead of silently
# double-serving. The schemas + ORM models still live under
# packages/otter/src/dcim/{schemas,models}/ipam.py — they back DB
# rows the surviving non-IPAM Python paths still read.
# /api/v1/dns/* fully moved to otter-go (PR 33). The bundle endpoint
# the collector pulls from (GET /servers/{id}/bundle) ported over PRs
# #247-#255; before that the zones/records/keys/health-checks/anycast-
# bindings/blocklists/views/forwarders/catalog-zones surface was
# already on Go. Python no longer registers the router so a misrouted
# internal call fails loud. Recursive servers return 501 from the
# bundle endpoint until the GoBGP + RPZ + recursive engine helpers
# port in a follow-up; auth servers are feature-complete.
# /api/v1/bgp/* fully moved to otter-go (PRs #203 + #204 for TCP-AO;
# this PR for the rest — asns/prefix-lists/community-lists/route-maps
# + their entries; the Go handlers were already implemented in PR 44
# but stayed dark until the ingress cutover landed here).
router.include_router(regiondeploy_router)
