"""Driver base class + Sample dataclass."""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime

from ..config import DeviceCfg


@dataclass
class Sample:
    asset_id: str
    metric: str
    value: float
    ts: datetime
    unit: str | None = None
    tags: dict[str, str] = field(default_factory=dict)


class Driver:
    """Subclasses implement `async def poll(device) -> list[Sample]`."""

    name: str = "base"

    async def poll(self, device: DeviceCfg) -> list[Sample]:  # pragma: no cover
        raise NotImplementedError
