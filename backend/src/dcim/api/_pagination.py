"""Generic SELECT pagination + filter + sort helper.

Designed for enterprise lists where the caller gives field names rather than crafting SQL.
"""

from __future__ import annotations

from typing import Any

from sqlalchemy import Select, asc, desc, func, select
from sqlalchemy.ext.asyncio import AsyncSession

from ..schemas.common import Page, PageParams, SortOrder


async def paginate(
    db: AsyncSession,
    base_stmt: Select,
    *,
    model: type,
    params: PageParams,
    out_model: type,
    allowed_sorts: set[str] | None = None,
) -> Page[Any]:
    if params.sort:
        from ..errors import ValidationError
        if allowed_sorts is not None and params.sort not in allowed_sorts:
            raise ValidationError(f"sort field not allowed: {params.sort}")
        col = getattr(model, params.sort, None)
        if col is None:
            raise ValidationError(f"unknown sort field: {params.sort}")
        base_stmt = base_stmt.order_by(asc(col) if params.order == SortOrder.asc else desc(col))

    total_stmt = select(func.count()).select_from(base_stmt.order_by(None).subquery())
    total = (await db.execute(total_stmt)).scalar_one()

    rows = (
        await db.execute(
            base_stmt.offset((params.page - 1) * params.page_size).limit(params.page_size)
        )
    ).scalars().all()

    return Page[out_model](  # type: ignore[valid-type]
        items=[out_model.model_validate(r) for r in rows],
        page=params.page,
        page_size=params.page_size,
        total=int(total or 0),
        has_more=(params.page * params.page_size) < int(total or 0),
    )
