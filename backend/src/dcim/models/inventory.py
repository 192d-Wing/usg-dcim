"""Physical inventory hierarchy and relationships.

Hierarchy: Region → Site → Building → Room → Row → Rack → Asset.
SiteGroup gives an orthogonal logical grouping (MAJCOM, mission, enclave, etc.).
"""

from __future__ import annotations

import enum
from typing import TYPE_CHECKING
from uuid import UUID

from sqlalchemy import (
    JSON,
    CheckConstraint,
    Enum,
    ForeignKey,
    Index,
    Integer,
    Numeric,
    String,
    UniqueConstraint,
)
from sqlalchemy.dialects.postgresql import UUID as PgUUID
from sqlalchemy.orm import Mapped, mapped_column, relationship

from ..db import Base
from ._mixins import Timestamped, UUIDPrimaryKey

if TYPE_CHECKING:
    pass


class LifecycleState(str, enum.Enum):
    planned = "planned"
    staged = "staged"
    active = "active"
    maintenance = "maintenance"
    decommissioned = "decommissioned"
    retired = "retired"


class AssetKind(str, enum.Enum):
    server = "server"
    switch = "switch"
    router = "router"
    pdu = "pdu"
    ups = "ups"
    crac = "crac"
    sensor = "sensor"
    storage = "storage"
    chassis = "chassis"
    blade = "blade"
    other = "other"


class AssetFace(str, enum.Enum):
    """Which side of the rack the asset is mounted on."""
    front = "front"
    rear = "rear"


class AssetMount(str, enum.Enum):
    """How the asset attaches to the rack.

    `rack` — standard rack-mount; uses rack_position_u + rack_units in the U-grid.
    `vertical-left` / `vertical-right` — 0U vertical PDU on the side rails;
    spans the full rack height visually and ignores rack_position_u.
    """
    rack = "rack"
    vertical_left = "vertical-left"
    vertical_right = "vertical-right"


class PduSide(str, enum.Enum):
    """A/B feed designation on a PDU (used to classify redundancy)."""
    a = "A"
    b = "B"
    c = "C"


class Region(UUIDPrimaryKey, Timestamped, Base):
    __tablename__ = "regions"

    name: Mapped[str] = mapped_column(String(128), nullable=False, unique=True)
    code: Mapped[str] = mapped_column(String(32), nullable=False, unique=True)
    description: Mapped[str | None] = mapped_column(String(512))

    sites: Mapped[list[Site]] = relationship(back_populates="region")


class Site(UUIDPrimaryKey, Timestamped, Base):
    __tablename__ = "sites"
    __table_args__ = (
        UniqueConstraint("region_id", "code", name="uq_site_region_code"),
        Index("ix_sites_region", "region_id"),
        Index("ix_sites_lifecycle", "lifecycle_state"),
    )

    region_id: Mapped[UUID] = mapped_column(PgUUID(as_uuid=True), ForeignKey("regions.id"), nullable=False)
    name: Mapped[str] = mapped_column(String(128), nullable=False)
    code: Mapped[str] = mapped_column(String(32), nullable=False)
    address: Mapped[str | None] = mapped_column(String(512))
    latitude: Mapped[float | None] = mapped_column(Numeric(9, 6))
    longitude: Mapped[float | None] = mapped_column(Numeric(9, 6))
    timezone: Mapped[str | None] = mapped_column(String(64))
    majcom: Mapped[str | None] = mapped_column(String(64), index=True)
    organization: Mapped[str | None] = mapped_column(String(128), index=True)
    mission_owner: Mapped[str | None] = mapped_column(String(128), index=True)
    enclave: Mapped[str | None] = mapped_column(String(64), index=True)
    classification: Mapped[str | None] = mapped_column(String(32))
    lifecycle_state: Mapped[LifecycleState] = mapped_column(
        Enum(LifecycleState, name="lifecycle_state"), default=LifecycleState.active, nullable=False
    )
    metadata_json: Mapped[dict] = mapped_column(JSON, default=dict, nullable=False)

    region: Mapped[Region] = relationship(back_populates="sites")
    buildings: Mapped[list[Building]] = relationship(back_populates="site")
    group_memberships: Mapped[list[SiteGroupMembership]] = relationship(back_populates="site")


class SiteGroup(UUIDPrimaryKey, Timestamped, Base):
    """Orthogonal grouping of sites — MAJCOM, mission, enclave, custom collections."""

    __tablename__ = "site_groups"

    name: Mapped[str] = mapped_column(String(128), nullable=False, unique=True)
    kind: Mapped[str] = mapped_column(String(32), nullable=False)  # majcom|mission|enclave|custom
    description: Mapped[str | None] = mapped_column(String(512))

    memberships: Mapped[list[SiteGroupMembership]] = relationship(back_populates="group")


class SiteGroupMembership(UUIDPrimaryKey, Timestamped, Base):
    __tablename__ = "site_group_memberships"
    __table_args__ = (UniqueConstraint("site_id", "group_id", name="uq_site_group_member"),)

    site_id: Mapped[UUID] = mapped_column(PgUUID(as_uuid=True), ForeignKey("sites.id"), nullable=False)
    group_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("site_groups.id"), nullable=False
    )

    site: Mapped[Site] = relationship(back_populates="group_memberships")
    group: Mapped[SiteGroup] = relationship(back_populates="memberships")


class Building(UUIDPrimaryKey, Timestamped, Base):
    __tablename__ = "buildings"
    __table_args__ = (
        UniqueConstraint("site_id", "code", name="uq_building_site_code"),
        Index("ix_buildings_site", "site_id"),
    )

    site_id: Mapped[UUID] = mapped_column(PgUUID(as_uuid=True), ForeignKey("sites.id"), nullable=False)
    name: Mapped[str] = mapped_column(String(128), nullable=False)
    code: Mapped[str] = mapped_column(String(32), nullable=False)

    site: Mapped[Site] = relationship(back_populates="buildings")
    rooms: Mapped[list[Room]] = relationship(back_populates="building")


class Room(UUIDPrimaryKey, Timestamped, Base):
    __tablename__ = "rooms"
    __table_args__ = (
        UniqueConstraint("building_id", "code", name="uq_room_building_code"),
        Index("ix_rooms_building", "building_id"),
    )

    building_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("buildings.id"), nullable=False
    )
    name: Mapped[str] = mapped_column(String(128), nullable=False)
    code: Mapped[str] = mapped_column(String(32), nullable=False)
    floor_area_sqft: Mapped[int | None] = mapped_column(Integer)
    design_kw: Mapped[float | None] = mapped_column(Numeric(10, 2))
    design_cooling_tons: Mapped[float | None] = mapped_column(Numeric(10, 2))

    building: Mapped[Building] = relationship(back_populates="rooms")
    rows: Mapped[list[Row]] = relationship(back_populates="room")


class Row(UUIDPrimaryKey, Timestamped, Base):
    __tablename__ = "rows"
    __table_args__ = (
        UniqueConstraint("room_id", "code", name="uq_row_room_code"),
        Index("ix_rows_room", "room_id"),
    )

    room_id: Mapped[UUID] = mapped_column(PgUUID(as_uuid=True), ForeignKey("rooms.id"), nullable=False)
    name: Mapped[str] = mapped_column(String(64), nullable=False)
    code: Mapped[str] = mapped_column(String(32), nullable=False)

    room: Mapped[Room] = relationship(back_populates="rows")
    racks: Mapped[list[Rack]] = relationship(back_populates="row")


class Rack(UUIDPrimaryKey, Timestamped, Base):
    __tablename__ = "racks"
    __table_args__ = (
        UniqueConstraint("row_id", "code", name="uq_rack_row_code"),
        Index("ix_racks_site", "site_id"),
        Index("ix_racks_row", "row_id"),
        CheckConstraint("u_height > 0 AND u_height <= 60", name="ck_rack_u_height"),
    )

    site_id: Mapped[UUID] = mapped_column(PgUUID(as_uuid=True), ForeignKey("sites.id"), nullable=False)
    row_id: Mapped[UUID] = mapped_column(PgUUID(as_uuid=True), ForeignKey("rows.id"), nullable=False)
    name: Mapped[str] = mapped_column(String(64), nullable=False)
    code: Mapped[str] = mapped_column(String(32), nullable=False)
    u_height: Mapped[int] = mapped_column(Integer, default=42, nullable=False)
    max_kw: Mapped[float | None] = mapped_column(Numeric(8, 2))
    max_weight_lbs: Mapped[int | None] = mapped_column(Integer)
    serial: Mapped[str | None] = mapped_column(String(128))

    row: Mapped[Row] = relationship(back_populates="racks")
    assets: Mapped[list[Asset]] = relationship(back_populates="rack")


class Asset(UUIDPrimaryKey, Timestamped, Base):
    """A physical or logical device — server, switch, PDU, sensor, UPS, CRAC, etc."""

    __tablename__ = "assets"
    __table_args__ = (
        Index("ix_assets_site", "site_id"),
        Index("ix_assets_rack", "rack_id"),
        Index("ix_assets_kind", "kind"),
        Index("ix_assets_lifecycle", "lifecycle_state"),
        Index("ix_assets_serial", "serial"),
        Index("ix_assets_hostname", "hostname"),
        UniqueConstraint("serial", "manufacturer", name="uq_asset_serial_manufacturer"),
    )

    site_id: Mapped[UUID] = mapped_column(PgUUID(as_uuid=True), ForeignKey("sites.id"), nullable=False)
    rack_id: Mapped[UUID | None] = mapped_column(PgUUID(as_uuid=True), ForeignKey("racks.id"))
    parent_asset_id: Mapped[UUID | None] = mapped_column(PgUUID(as_uuid=True), ForeignKey("assets.id"))

    name: Mapped[str] = mapped_column(String(255), nullable=False)
    hostname: Mapped[str | None] = mapped_column(String(255))
    kind: Mapped[AssetKind] = mapped_column(Enum(AssetKind, name="asset_kind"), nullable=False)
    manufacturer: Mapped[str | None] = mapped_column(String(128))
    model: Mapped[str | None] = mapped_column(String(128))
    serial: Mapped[str | None] = mapped_column(String(128))
    firmware: Mapped[str | None] = mapped_column(String(64))

    rack_position_u: Mapped[int | None] = mapped_column(Integer)
    rack_units: Mapped[int | None] = mapped_column(Integer, default=1)
    # values_callable forces SQLAlchemy to persist the enum VALUE (e.g. "vertical-left")
    # rather than the Python identifier ("vertical_left"), matching the DDL.
    face: Mapped[AssetFace] = mapped_column(
        Enum(AssetFace, name="asset_face", values_callable=lambda x: [e.value for e in x]),
        default=AssetFace.front, nullable=False,
    )
    mount: Mapped[AssetMount] = mapped_column(
        Enum(AssetMount, name="asset_mount", values_callable=lambda x: [e.value for e in x]),
        default=AssetMount.rack, nullable=False,
    )
    pdu_side: Mapped[PduSide | None] = mapped_column(
        Enum(PduSide, name="pdu_side", values_callable=lambda x: [e.value for e in x])
    )
    # Devices only: how many independent PSUs the device has (for redundancy gap detection)
    psu_count: Mapped[int | None] = mapped_column(Integer)

    mgmt_ip: Mapped[str | None] = mapped_column(String(64))
    mgmt_protocol: Mapped[str | None] = mapped_column(String(16))  # snmp|redfish|modbus|rest|ipmi
    mgmt_port: Mapped[int | None] = mapped_column(Integer)
    mgmt_credentials_ref: Mapped[str | None] = mapped_column(String(128))  # opaque key into secrets

    lifecycle_state: Mapped[LifecycleState] = mapped_column(
        Enum(LifecycleState, name="lifecycle_state"), default=LifecycleState.active, nullable=False
    )
    install_date: Mapped[str | None] = mapped_column(String(32))
    warranty_expires: Mapped[str | None] = mapped_column(String(32))
    metadata_json: Mapped[dict] = mapped_column(JSON, default=dict, nullable=False)

    rack: Mapped[Rack | None] = relationship(back_populates="assets")
    parent: Mapped[Asset | None] = relationship(remote_side="Asset.id")


class PowerFeed(UUIDPrimaryKey, Timestamped, Base):
    __tablename__ = "power_feeds"
    __table_args__ = (
        Index("ix_power_feeds_site", "site_id"),
        Index("ix_power_feeds_rack", "rack_id"),
    )

    site_id: Mapped[UUID] = mapped_column(PgUUID(as_uuid=True), ForeignKey("sites.id"), nullable=False)
    rack_id: Mapped[UUID | None] = mapped_column(PgUUID(as_uuid=True), ForeignKey("racks.id"))
    name: Mapped[str] = mapped_column(String(128), nullable=False)
    side: Mapped[str | None] = mapped_column(String(8))  # A|B
    voltage: Mapped[int | None] = mapped_column(Integer)
    amperage: Mapped[int | None] = mapped_column(Integer)
    phase: Mapped[str | None] = mapped_column(String(8))
    upstream_pdu_id: Mapped[UUID | None] = mapped_column(PgUUID(as_uuid=True), ForeignKey("assets.id"))


class Circuit(UUIDPrimaryKey, Timestamped, Base):
    __tablename__ = "circuits"
    __table_args__ = (Index("ix_circuits_site", "site_id"),)

    site_id: Mapped[UUID] = mapped_column(PgUUID(as_uuid=True), ForeignKey("sites.id"), nullable=False)
    label: Mapped[str] = mapped_column(String(128), nullable=False)
    provider: Mapped[str | None] = mapped_column(String(128))
    bandwidth_mbps: Mapped[int | None] = mapped_column(Integer)
    purpose: Mapped[str | None] = mapped_column(String(128))


class Cable(UUIDPrimaryKey, Timestamped, Base):
    __tablename__ = "cables"
    __table_args__ = (
        Index("ix_cables_site", "site_id"),
        Index("ix_cables_a_end", "a_asset_id"),
        Index("ix_cables_b_end", "b_asset_id"),
    )

    site_id: Mapped[UUID] = mapped_column(PgUUID(as_uuid=True), ForeignKey("sites.id"), nullable=False)
    a_asset_id: Mapped[UUID] = mapped_column(PgUUID(as_uuid=True), ForeignKey("assets.id"), nullable=False)
    a_port: Mapped[str | None] = mapped_column(String(64))
    b_asset_id: Mapped[UUID] = mapped_column(PgUUID(as_uuid=True), ForeignKey("assets.id"), nullable=False)
    b_port: Mapped[str | None] = mapped_column(String(64))
    medium: Mapped[str | None] = mapped_column(String(32))  # cat6|smf|mmf|power-c13|...
    color: Mapped[str | None] = mapped_column(String(16))
    length_m: Mapped[float | None] = mapped_column(Numeric(6, 2))
    label: Mapped[str | None] = mapped_column(String(128))
