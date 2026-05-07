"""Shared request/response shapes — pagination, sorting, bulk envelopes."""

from __future__ import annotations

import enum
from typing import Generic, TypeVar

from fastapi import Query
from pydantic import BaseModel, Field

T = TypeVar("T")


class SortOrder(str, enum.Enum):
    asc = "asc"
    desc = "desc"


class PageParams(BaseModel):
    """Cursor-aware pagination knobs. Default page-size is enterprise-safe."""

    page: int = Field(1, ge=1)
    page_size: int = Field(50, ge=1, le=500)
    sort: str | None = None
    order: SortOrder = SortOrder.asc

    @classmethod
    def from_query(
        cls,
        page: int = Query(1, ge=1),
        page_size: int = Query(50, ge=1, le=500),
        sort: str | None = Query(None),
        order: SortOrder = Query(SortOrder.asc),
    ) -> PageParams:
        return cls(page=page, page_size=page_size, sort=sort, order=order)


class Page(BaseModel, Generic[T]):
    items: list[T]
    page: int
    page_size: int
    total: int
    has_more: bool


class BulkResult(BaseModel):
    inserted: int = 0
    updated: int = 0
    skipped: int = 0
    failed: int = 0
    errors: list[dict] = Field(default_factory=list)
