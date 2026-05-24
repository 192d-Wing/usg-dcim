"""Organization registry — the entity that owns ASNs, IP allocations,
and (eventually) other ARIN-tracked resources.

Field shape follows the ARIN Org template + POC templates closely so
operators can copy/paste the same record into ARIN Online when
registering a new resource:

  https://www.arin.net/resources/registry/templates/

POCs are embedded here as four sets of (name, email, phone) columns
rather than a separate PointOfContact table. The downside is that POCs
can't be shared across orgs; the upside is that the read path for "show
me this org's admin POC" is a single row fetch. v1 trades the
shareability for simplicity; we can extract POCs later if we end up
with the same person on many orgs.
"""

from __future__ import annotations

from sqlalchemy import String
from sqlalchemy.orm import Mapped, mapped_column

from ..db import Base
from ._mixins import Timestamped, UUIDPrimaryKey


class Organization(UUIDPrimaryKey, Timestamped, Base):
    __tablename__ = "organizations"

    # Legal name — ARIN's OrgName field. Required.
    name: Mapped[str] = mapped_column(String(256), nullable=False)
    # ARIN-assigned handle (e.g. "EXAMPLE-1"). Nullable because operators
    # often want to track an org locally before they've registered it.
    arin_org_id: Mapped[str | None] = mapped_column(String(64))

    # Postal address. ARIN requires line1 + city + country; state/postal
    # are required for US/CA but we leave the schema permissive and let
    # the UI/validators enforce per-country.
    address_line1: Mapped[str] = mapped_column(String(256), nullable=False)
    address_line2: Mapped[str | None] = mapped_column(String(256))
    city: Mapped[str] = mapped_column(String(128), nullable=False)
    state_province: Mapped[str | None] = mapped_column(String(64))
    postal_code: Mapped[str | None] = mapped_column(String(32))
    country: Mapped[str] = mapped_column(String(2), nullable=False)

    # Org-level contact info.
    phone: Mapped[str | None] = mapped_column(String(64))
    email: Mapped[str | None] = mapped_column(String(256))

    # POCs — admin / tech / abuse are required by ARIN at registration.
    # name + email are the structurally meaningful fields; phone is
    # ARIN-recommended but not always required.
    admin_poc_name: Mapped[str] = mapped_column(String(128), nullable=False)
    admin_poc_email: Mapped[str] = mapped_column(String(256), nullable=False)
    admin_poc_phone: Mapped[str | None] = mapped_column(String(64))

    tech_poc_name: Mapped[str] = mapped_column(String(128), nullable=False)
    tech_poc_email: Mapped[str] = mapped_column(String(256), nullable=False)
    tech_poc_phone: Mapped[str | None] = mapped_column(String(64))

    abuse_poc_name: Mapped[str] = mapped_column(String(128), nullable=False)
    abuse_poc_email: Mapped[str] = mapped_column(String(256), nullable=False)
    abuse_poc_phone: Mapped[str | None] = mapped_column(String(64))

    # NOC POC is optional — ARIN supports it but doesn't require it.
    noc_poc_name: Mapped[str | None] = mapped_column(String(128))
    noc_poc_email: Mapped[str | None] = mapped_column(String(256))
    noc_poc_phone: Mapped[str | None] = mapped_column(String(64))

    description: Mapped[str | None] = mapped_column(String(512))
