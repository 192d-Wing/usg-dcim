"""TimescaleDB hypertable for raw telemetry samples.

Declared as a Core Table (not declarative ORM) because hypertables don't have
a single-column primary key and the ORM's identity map fights that. Reads and
writes go through raw SQL in `services.telemetry` — this Table object exists
so Alembic autogenerate sees the schema and so the codebase has one canonical
column definition.
"""

from sqlalchemy import Column, Double, Index, Integer, Table, Text, UniqueConstraint
from sqlalchemy.dialects.postgresql import JSONB, TIMESTAMP, UUID

from ..db import Base

telemetry_samples = Table(
    "telemetry_samples",
    Base.metadata,
    Column("ts",           TIMESTAMP(timezone=True), nullable=False),
    Column("site_id",      UUID(as_uuid=True), nullable=False),
    Column("asset_id",     UUID(as_uuid=True), nullable=False),
    Column("collector_id", UUID(as_uuid=True), nullable=False),
    Column("batch_id",     Text, nullable=False),
    Column("seq",          Integer, nullable=False),
    Column("metric",       Text, nullable=False),
    Column("value",        Double, nullable=False),
    Column("unit",         Text),
    Column("received_at",  TIMESTAMP(timezone=True), nullable=False),
    Column("tags",         JSONB, nullable=False, server_default="'{}'"),
    UniqueConstraint("collector_id", "batch_id", "seq", "ts",
                     name="uq_telem_sample_dedup"),
    Index("ix_telem_samples_asset_metric", "asset_id", "metric", "ts"),
    Index("ix_telem_samples_site_metric",  "site_id",  "metric", "ts"),
)
