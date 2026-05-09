"""PDU outlets and power connections.

A PDU has N Outlets. Each Outlet may carry a PowerConnection that ties it to a
specific PSU on a powered device. With this model we can classify redundancy:
a device is `redundant` if its PSUs are spread across PDUs on different sides
(A and B), `single` if it has exactly one connection or all connections to the
same side, and `unpowered` if it has none.
"""

from __future__ import annotations

from uuid import UUID

from sqlalchemy import (
    Enum,
    ForeignKey,
    Index,
    Integer,
    String,
    UniqueConstraint,
)
from sqlalchemy.dialects.postgresql import UUID as PgUUID
from sqlalchemy.orm import Mapped, mapped_column

from ..db import Base
from ._mixins import Timestamped, UUIDPrimaryKey
from .inventory import PduSide


class Outlet(UUIDPrimaryKey, Timestamped, Base):
    """A single physical outlet on a PDU asset."""

    __tablename__ = "outlets"
    __table_args__ = (
        UniqueConstraint("pdu_asset_id", "position", name="uq_outlet_pdu_position"),
        Index("ix_outlets_pdu", "pdu_asset_id"),
    )

    pdu_asset_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("assets.id", ondelete="CASCADE"), nullable=False
    )
    position: Mapped[int] = mapped_column(Integer, nullable=False)
    label: Mapped[str | None] = mapped_column(String(32))   # vendor-printed label e.g. "1A"
    phase: Mapped[PduSide | None] = mapped_column(
        Enum(PduSide, name="pdu_side", values_callable=lambda x: [e.value for e in x])
    )
    max_amps: Mapped[int | None] = mapped_column(Integer)
    receptacle: Mapped[str | None] = mapped_column(String(16))  # C13, C19, NEMA 5-15R, etc.


class PowerConnection(UUIDPrimaryKey, Timestamped, Base):
    """A cord run from an outlet to a specific PSU on a device."""

    __tablename__ = "power_connections"
    __table_args__ = (
        UniqueConstraint("outlet_id", name="uq_power_connection_outlet"),
        Index("ix_power_connections_asset_psu", "asset_id", "psu_index"),
    )

    outlet_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("outlets.id", ondelete="CASCADE"), nullable=False
    )
    asset_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("assets.id", ondelete="CASCADE"), nullable=False
    )
    psu_index: Mapped[int] = mapped_column(Integer, default=1, nullable=False)
    cord_color: Mapped[str | None] = mapped_column(String(16))
    cord_length_m: Mapped[float | None] = mapped_column()
