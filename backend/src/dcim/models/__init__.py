"""SQLAlchemy ORM models. Import here so Alembic autogenerate sees them."""

from .alerts import Alert, AlertRule, MaintenanceWindow
from .audit import AuditLog
from .auth import ApiToken, Permission, Role, RoleScope, User, UserRole
from .collectors import Collector, CollectorHeartbeat
from .inventory import (
    Asset,
    AssetFace,
    AssetMount,
    Building,
    Cable,
    Circuit,
    PduSide,
    PowerFeed,
    Rack,
    Region,
    Room,
    Row,
    Site,
    SiteGroup,
    SiteGroupMembership,
)
from .notifications import ChannelKind, NotificationChannel
from .power import Outlet, PowerConnection
from .telemetry_meta import TelemetrySource

__all__ = [
    "Alert",
    "AlertRule",
    "ApiToken",
    "Asset",
    "AssetFace",
    "AssetMount",
    "AuditLog",
    "Building",
    "Cable",
    "ChannelKind",
    "Circuit",
    "Collector",
    "CollectorHeartbeat",
    "MaintenanceWindow",
    "NotificationChannel",
    "Outlet",
    "PduSide",
    "Permission",
    "PowerConnection",
    "PowerFeed",
    "Rack",
    "Region",
    "Role",
    "RoleScope",
    "Room",
    "Row",
    "Site",
    "SiteGroup",
    "SiteGroupMembership",
    "TelemetrySource",
    "User",
    "UserRole",
]
