"""Canonical capability codes and built-in role bundles.

Capabilities are flat strings consumed by `require_capability(...)`. Roles are
bundles of capabilities; ABAC scope is layered on top via RoleScope rows.
"""

from __future__ import annotations

# --- inventory plane ---
INVENTORY_READ = "inventory:read"
INVENTORY_WRITE = "inventory:write"
INVENTORY_BULK = "inventory:bulk"

# --- collector plane ---
COLLECTOR_READ = "collector:read"
COLLECTOR_WRITE = "collector:write"
COLLECTOR_ENROLL = "collector:enroll"
COLLECTOR_INGEST = "collector:ingest"

# --- telemetry / dashboards ---
TELEMETRY_READ = "telemetry:read"
DASHBOARD_READ = "dashboard:read"

# --- alerting ---
ALERTS_READ = "alerts:read"
ALERTS_ACK = "alerts:ack"
ALERTS_CONFIGURE = "alerts:configure"

# --- power control (separately permissioned + audited) ---
POWER_CONTROL = "power:control"
POWER_APPROVE = "power:approve"

# --- audit / governance ---
AUDIT_READ = "audit:read"

# --- admin ---
USERS_MANAGE = "users:manage"
ROLES_MANAGE = "roles:manage"
TOKENS_MANAGE = "tokens:manage"
SITES_MANAGE = "sites:manage"


BUILT_IN_ROLES: dict[str, list[str]] = {
    "EnterpriseAdmin": [
        INVENTORY_READ, INVENTORY_WRITE, INVENTORY_BULK,
        COLLECTOR_READ, COLLECTOR_WRITE, COLLECTOR_ENROLL, COLLECTOR_INGEST,
        TELEMETRY_READ, DASHBOARD_READ,
        ALERTS_READ, ALERTS_ACK, ALERTS_CONFIGURE,
        POWER_CONTROL, POWER_APPROVE,
        AUDIT_READ,
        USERS_MANAGE, ROLES_MANAGE, TOKENS_MANAGE, SITES_MANAGE,
    ],
    "RegionalAdmin": [
        INVENTORY_READ, INVENTORY_WRITE,
        COLLECTOR_READ, COLLECTOR_WRITE,
        TELEMETRY_READ, DASHBOARD_READ,
        ALERTS_READ, ALERTS_ACK, ALERTS_CONFIGURE,
        POWER_APPROVE,
        AUDIT_READ,
        TOKENS_MANAGE,
    ],
    "SiteAdmin": [
        INVENTORY_READ, INVENTORY_WRITE,
        COLLECTOR_READ, COLLECTOR_WRITE,
        TELEMETRY_READ, DASHBOARD_READ,
        ALERTS_READ, ALERTS_ACK, ALERTS_CONFIGURE,
        POWER_APPROVE,
    ],
    "DataCenterManager": [
        INVENTORY_READ, INVENTORY_WRITE,
        TELEMETRY_READ, DASHBOARD_READ,
        ALERTS_READ, ALERTS_ACK,
    ],
    "Technician": [
        INVENTORY_READ, INVENTORY_WRITE,
        TELEMETRY_READ, DASHBOARD_READ,
        ALERTS_READ, ALERTS_ACK,
    ],
    "PowerOperator": [
        INVENTORY_READ,
        TELEMETRY_READ, DASHBOARD_READ,
        ALERTS_READ,
        POWER_CONTROL,
    ],
    "Auditor": [
        INVENTORY_READ,
        TELEMETRY_READ, DASHBOARD_READ,
        ALERTS_READ,
        AUDIT_READ,
    ],
    "Viewer": [
        INVENTORY_READ,
        TELEMETRY_READ, DASHBOARD_READ,
        ALERTS_READ,
    ],
    "ApiServiceAccount": [
        INVENTORY_READ, COLLECTOR_INGEST, TELEMETRY_READ,
    ],
}
