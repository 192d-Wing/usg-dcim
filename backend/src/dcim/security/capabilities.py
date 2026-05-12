"""Canonical capability codes, granular IAM-style catalog, and built-in role bundles.

Capability format: `<domain>:<resource>:<action>` (e.g. `dns:zones:read`).
A handful of specialty codes are 2-segment (`power:control`, `power:approve`).

Wildcard matching in `require_capability`:
  *                   matches anything
  <domain>:*          matches any capability under that domain
  <domain>:<r>:*      matches any action on that resource

The old 2-segment codes (e.g. `inventory:read`) are preserved as
constants for endpoint-gate backward compatibility. Phases 1–4 of the
RBAC refactor tighten each gate to the specific granular code; until
then, the 2-segment names continue to work because the migration
keeps them alongside the expanded granular set on every role.
"""

from __future__ import annotations

# --- Legacy 2-segment codes (still referenced by un-tightened gates) -----
# Kept verbatim so phases 1–4 can replace them route-by-route.

# inventory plane
INVENTORY_READ = "inventory:read"
INVENTORY_WRITE = "inventory:write"
INVENTORY_BULK = "inventory:bulk"

# collector plane
COLLECTOR_READ = "collector:read"
COLLECTOR_WRITE = "collector:write"
COLLECTOR_ENROLL = "collector:enroll"
COLLECTOR_INGEST = "collector:ingest"

# telemetry / dashboards
TELEMETRY_READ = "telemetry:read"
DASHBOARD_READ = "dashboard:read"

# alerting
ALERTS_READ = "alerts:read"
ALERTS_ACK = "alerts:ack"
ALERTS_CONFIGURE = "alerts:configure"

# power control (separately permissioned + audited)
POWER_CONTROL = "power:control"
POWER_APPROVE = "power:approve"

# audit / governance
AUDIT_READ = "audit:read"

# admin
USERS_MANAGE = "users:manage"
ROLES_MANAGE = "roles:manage"
TOKENS_MANAGE = "tokens:manage"
SITES_MANAGE = "sites:manage"


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
        "bulk": ["execute"],
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
        "dhcp-servers": ["create", "read", "update", "delete"],
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
    "audit": {
        "events": ["read", "export"],
    },
    "admin": {
        "users": ["create", "read", "update", "delete"],
        "roles": ["create", "read", "update", "delete"],
        "oidc-mappings": ["create", "read", "update", "delete"],
        "api-tokens": ["create", "read", "update", "delete"],
    },
    "notifications": {
        "channels": ["create", "read", "update", "delete"],
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


def all_granular_codes() -> set[str]:
    """Every code derivable from the catalog + specialty list."""
    out: set[str] = set(SPECIALTY_CAPABILITIES)
    for domain, resources in CAPABILITY_CATALOG.items():
        for resource, actions in resources.items():
            for action in actions:
                out.add(f"{domain}:{resource}:{action}")
    return out


# --- Old-code -> granular expansion --------------------------------------
# Applied by the 20260512_0030 migration so existing roles preserve their
# effective access while gaining the new granular names. Each entry is
# the set of granular codes that an old code logically implied.

def _crud_codes(domain: str, *resources: str, actions: tuple[str, ...] = ("create", "read", "update", "delete")) -> list[str]:
    return [f"{domain}:{r}:{a}" for r in resources for a in actions]


# All inventory CRUD resources (everything in CAPABILITY_CATALOG["inventory"] except `bulk`).
_INV_RESOURCES = ("sites", "regions", "buildings", "rooms", "rows", "racks", "assets", "stencils")

LEGACY_CODE_EXPANSION: dict[str, list[str]] = {
    INVENTORY_READ: _crud_codes("inventory", *_INV_RESOURCES, actions=("read",)),
    INVENTORY_WRITE: _crud_codes("inventory", *_INV_RESOURCES, actions=("create", "update", "delete")),
    INVENTORY_BULK: ["inventory:bulk:execute"],
    COLLECTOR_READ: ["collectors:collectors:read"],
    COLLECTOR_WRITE: ["collectors:collectors:create", "collectors:collectors:update", "collectors:collectors:delete"],
    COLLECTOR_ENROLL: ["collectors:collectors:enroll"],
    COLLECTOR_INGEST: ["collectors:ingest:write"],
    TELEMETRY_READ: ["telemetry:metrics:read", "telemetry:events:read"],
    DASHBOARD_READ: ["dashboards:dashboards:read"],
    ALERTS_READ: ["alerts:alerts:read", "alerts:rules:read", "alerts:silences:read"],
    ALERTS_ACK: ["alerts:alerts:ack"],
    ALERTS_CONFIGURE: _crud_codes("alerts", "rules", "silences", actions=("create", "update", "delete")),
    POWER_CONTROL: ["power:control"],
    POWER_APPROVE: ["power:approve"],
    AUDIT_READ: ["audit:events:read"],
    USERS_MANAGE: _crud_codes("admin", "users", actions=("create", "read", "update", "delete")),
    ROLES_MANAGE: _crud_codes("admin", "roles", "oidc-mappings", actions=("create", "read", "update", "delete")),
    TOKENS_MANAGE: _crud_codes("admin", "api-tokens", actions=("create", "read", "update", "delete")),
    SITES_MANAGE: _crud_codes("inventory", "sites", actions=("create", "update", "delete")),
}


def expand_legacy(codes: list[str]) -> list[str]:
    """Return `codes` plus every granular code each legacy code implies.

    Deterministic ordering: original codes first (in input order, deduped),
    then expansions in registry order. Idempotent — calling twice yields
    the same set.
    """
    seen: set[str] = set()
    out: list[str] = []
    for c in codes:
        if c not in seen:
            seen.add(c)
            out.append(c)
    for c in codes:
        for granular in LEGACY_CODE_EXPANSION.get(c, []):
            if granular not in seen:
                seen.add(granular)
                out.append(granular)
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
    ],
    "SiteAdmin": [
        "inventory:racks:*", "inventory:rows:*", "inventory:rooms:*",
        "inventory:buildings:*", "inventory:assets:*",
        "inventory:sites:read", "inventory:regions:read", "inventory:stencils:read",
        _COLLECTORS_READ, "collectors:collectors:update",
        _TELEMETRY_ALL, _DASHBOARDS_READ,
        "alerts:*",
        POWER_APPROVE,
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
    ],
    "Viewer": [
        "inventory:*:read", "ipam:*:read", "dns:*:read",
        _COLLECTORS_READ, "alerts:*:read",
        _TELEMETRY_ALL, _DASHBOARDS_READ,
    ],
    "ApiServiceAccount": [
        "inventory:*:read",
        "collectors:ingest:write",
        _TELEMETRY_ALL,
    ],
}
