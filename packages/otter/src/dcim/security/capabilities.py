"""Canonical capability codes, granular IAM-style catalog, and built-in role bundles.

Capability format: `<domain>:<resource>:<action>` (e.g. `dns:zones:read`).
A handful of specialty codes are 2-segment (`power:control`, `power:approve`).

Wildcard matching in `require_capability`:
  *                   matches anything
  <domain>:*          matches any capability under that domain
  <domain>:<r>:*      matches any action on that resource
"""

from __future__ import annotations

# --- Canonical specialty constants ---------------------------------------
# 2-segment codes that don't fit the domain:resource:action shape. Kept
# as module-level constants because SPECIALTY_CAPABILITIES and several
# BUILT_IN_ROLES bundles reference them; using a constant avoids the
# duplicate-string-literal lint.

POWER_CONTROL = "power:control"
POWER_APPROVE = "power:approve"


# --- Granular catalog ----------------------------------------------------
# domain -> resource -> [actions]. Every (domain, resource, action) triple
# materializes a capability code of shape `domain:resource:action`.

CAPABILITY_CATALOG: dict[str, dict[str, list[str]]] = {
    "inventory": {
        "sites": ["create", "read", "update", "delete"],
        "regions": ["create", "read", "update", "delete"],
        "buildings": ["create", "read", "update", "delete"],
        "rooms": ["create", "read", "update", "delete"],
        "rows": ["create", "read", "update", "delete"],
        "racks": ["create", "read", "update", "delete"],
        "assets": ["create", "read", "update", "delete"],
        "cables": ["create", "read", "update", "delete"],
        "stencils": ["create", "read", "update", "delete"],
        "organizations": ["create", "read", "update", "delete"],
        "bulk": ["execute"],
    },
    "routing": {
        "asns": ["create", "read", "update", "delete"],
        "tcp-ao-key-chains": ["create", "read", "update", "delete", "rotate"],
        "tcp-ao-keys": ["create", "read", "update", "delete"],
        "prefix-lists": ["create", "read", "update", "delete"],
        "prefix-list-entries": ["create", "read", "update", "delete"],
        "community-lists": ["create", "read", "update", "delete"],
        "community-list-entries": ["create", "read", "update", "delete"],
        "route-maps": ["create", "read", "update", "delete"],
        "route-map-entries": ["create", "read", "update", "delete"],
    },
    "search": {
        "search": ["read"],
    },
    "ipam": {
        "fabrics": ["create", "read", "update", "delete"],
        "vrfs": ["create", "read", "update", "delete"],
        "vrf-bgp-peers": ["create", "read", "update", "delete"],
        "supernets": ["create", "read", "update", "delete"],
        "subnets": ["create", "read", "update", "delete"],
        "addresses": ["create", "read", "update", "delete"],
        "overlays": ["create", "read", "update", "delete"],
        "vnis": ["create", "read", "update", "delete"],
        "vteps": ["create", "read", "update", "delete"],
        "vtep-memberships": ["create", "read", "update", "delete"],
        "dhcp-servers": ["create", "read", "update", "delete", "bundle"],
        "dhcp-scopes": ["create", "read", "update", "delete", "push", "reconcile"],
        "dhcp-scope-templates": ["create", "read", "update", "delete"],
    },
    "dns": {
        "zones": ["create", "read", "update", "delete"],
        "records": ["create", "read", "update", "delete"],
        "servers": ["create", "read", "update", "delete", "bundle"],
        "keys": ["create", "read", "update", "delete", "rotate"],
        "forwarders": ["create", "read", "update", "delete"],
        "blocklists": ["create", "read", "update", "delete"],
        "views": ["create", "read", "update", "delete"],
        "health-checks": ["create", "read", "update", "delete"],
        "anycast-groups": ["create", "read", "update", "delete"],
        "anycast-bindings": ["create", "read", "update", "delete"],
        "bgp-peers": ["create", "read", "update", "delete"],
    },
    "collectors": {
        "collectors": ["create", "read", "update", "delete", "enroll"],
        "ingest": ["write"],
    },
    "alerts": {
        "alerts": ["read", "ack"],
        "rules": ["create", "read", "update", "delete"],
        "silences": ["create", "read", "update", "delete"],
    },
    "telemetry": {
        "metrics": ["read"],
        "events": ["read"],
    },
    "dashboards": {
        "dashboards": ["create", "read", "update", "delete"],
    },
    "maintenance": {
        "windows": ["create", "read", "update", "delete"],
    },
    "power": {
        "outlets": ["create", "read", "delete"],
    },
    "audit": {
        "events": ["read", "export"],
    },
    "admin": {
        "users": ["create", "read", "update", "delete"],
        "roles": ["create", "read", "update", "delete"],
        "oidc-mappings": ["create", "read", "update", "delete"],
        "api-tokens": ["create", "read", "update", "delete"],
        # Deployment-wide config rows in the system_settings table.
        # Today: DNS recursive upstreams override. Pattern is
        # generic so new settings (rate limits, defaults) don't
        # need their own catalog entry.
        "system-settings": ["read", "update"],
    },
    "notifications": {
        "channels": ["create", "read", "update", "delete"],
    },
    # Bare-metal cluster bring-up — see docs/dev/region-deploy.md.
    # `start`/`abort` are state-changing operations distinct from
    # plain `update`; `download-kubeconfig` is elevated because the
    # kubeconfig grants full cluster admin at the site.
    "infrastructure": {
        "region-deployments": [
            "create", "read", "update", "delete",
            "start", "abort", "download-kubeconfig",
        ],
    },
}

# Specialty codes that don't fit the resource:action shape. The picker UI
# renders these in a "Specialty" section per domain.
SPECIALTY_CAPABILITIES: dict[str, str] = {
    POWER_CONTROL: "Issue power-control commands to assets",
    POWER_APPROVE: "Approve pending power-control requests",
}

# Most-referenced granular codes — kept as constants so the role bundles
# below don't repeat the same string four or more times.
_DASHBOARDS_READ = "dashboards:dashboards:read"
_TELEMETRY_ALL = "telemetry:*"
_COLLECTORS_READ = "collectors:collectors:read"
_ALERTS_READ = "alerts:alerts:read"
_REGION_DEPLOY_READ = "infrastructure:region-deployments:read"


def all_granular_codes() -> set[str]:
    """Every code derivable from the catalog + specialty list."""
    out: set[str] = set(SPECIALTY_CAPABILITIES)
    for domain, resources in CAPABILITY_CATALOG.items():
        for resource, actions in resources.items():
            for action in actions:
                out.add(f"{domain}:{resource}:{action}")
    return out


# --- Built-in role bundles ----------------------------------------------
# Wildcards (`*`, `<domain>:*`) are recognized by require_capability and
# are how system roles stay forward-compatible: new capabilities added to
# the catalog are automatically granted to wildcard holders.

BUILT_IN_ROLES: dict[str, list[str]] = {
    "EnterpriseAdmin": ["*"],
    "RegionalAdmin": [
        "inventory:*",
        "ipam:*",
        "dns:zones:create", "dns:zones:read", "dns:zones:update",
        "dns:records:*", "dns:servers:read", "dns:servers:bundle",
        "dns:fabrics:read", "dns:upstreams:read", "dns:blocklists:*",
        "collectors:*",
        _TELEMETRY_ALL, "dashboards:*",
        "alerts:*",
        "audit:events:read",
        "admin:api-tokens:*",
        # Region-deploy: full lifecycle. download-kubeconfig is the
        # one elevated capability — gates access to the cluster's
        # admin kubeconfig.
        "infrastructure:region-deployments:*",
    ],
    "SiteAdmin": [
        "inventory:racks:*", "inventory:rows:*", "inventory:rooms:*",
        "inventory:buildings:*", "inventory:assets:*",
        "inventory:sites:read", "inventory:regions:read", "inventory:stencils:read",
        _COLLECTORS_READ, "collectors:collectors:update",
        _TELEMETRY_ALL, _DASHBOARDS_READ,
        "alerts:*",
        POWER_APPROVE,
        # Region-deploy: site-scoped operators can create + run + stop
        # deploys at their site, but not download the cluster
        # kubeconfig (kept to RegionalAdmin / EnterpriseAdmin).
        _REGION_DEPLOY_READ,
        "infrastructure:region-deployments:create",
        "infrastructure:region-deployments:start",
        "infrastructure:region-deployments:abort",
    ],
    "DataCenterManager": [
        "inventory:racks:*", "inventory:assets:*",
        "inventory:rooms:read", "inventory:rows:read", "inventory:sites:read",
        _TELEMETRY_ALL, _DASHBOARDS_READ,
        _ALERTS_READ, "alerts:alerts:ack", "alerts:rules:read",
    ],
    "Technician": [
        "inventory:racks:read", "inventory:racks:update",
        "inventory:assets:*",
        "inventory:rooms:read", "inventory:rows:read", "inventory:sites:read",
        _TELEMETRY_ALL, _DASHBOARDS_READ,
        _ALERTS_READ, "alerts:alerts:ack",
    ],
    "PowerOperator": [
        "inventory:racks:read", "inventory:assets:read",
        _TELEMETRY_ALL, _DASHBOARDS_READ,
        _ALERTS_READ,
        POWER_CONTROL,
    ],
    "Auditor": [
        "inventory:*:read", "ipam:*:read", "dns:*:read",
        _COLLECTORS_READ, "alerts:*:read",
        _TELEMETRY_ALL, _DASHBOARDS_READ,
        "maintenance:windows:read",
        "audit:events:read", "audit:events:export",
        # Region-deploy: read-only — auditors see history + event
        # streams but never trigger anything.
        _REGION_DEPLOY_READ,
    ],
    "Viewer": [
        "inventory:*:read", "ipam:*:read", "dns:*:read",
        _COLLECTORS_READ, "alerts:*:read",
        _TELEMETRY_ALL, _DASHBOARDS_READ,
        _REGION_DEPLOY_READ,
    ],
    "ApiServiceAccount": [
        "inventory:*:read",
        "collectors:ingest:write",
        _TELEMETRY_ALL,
    ],
}
