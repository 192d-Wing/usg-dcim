"""Local SQLite store-and-forward buffer.

Each enqueued sample row holds the JSON payload to POST upstream. The forwarder
drains in batches and removes successfully-acked rows. On crash/restart the
queue persists.
"""

from __future__ import annotations

import json
from datetime import UTC, datetime
from pathlib import Path
from typing import Any
from uuid import UUID, uuid4

import aiosqlite  # noqa: F401  (runtime dep; IDE may flag if venv missing)

_DDL = """
CREATE TABLE IF NOT EXISTS samples (
    id TEXT PRIMARY KEY,
    ts TEXT NOT NULL,
    asset_id TEXT NOT NULL,
    metric TEXT NOT NULL,
    value REAL NOT NULL,
    unit TEXT,
    tags_json TEXT
);
CREATE INDEX IF NOT EXISTS ix_samples_ts ON samples (ts);
CREATE TABLE IF NOT EXISTS meta (
    key TEXT PRIMARY KEY,
    value TEXT
);
"""


class Buffer:
    def __init__(self, path: str):
        Path(path).parent.mkdir(parents=True, exist_ok=True)
        self.path = path
        self._db: aiosqlite.Connection | None = None

    async def open(self) -> None:
        self._db = await aiosqlite.connect(self.path)
        await self._db.executescript(_DDL)
        await self._db.commit()

    async def close(self) -> None:
        if self._db is not None:
            await self._db.close()
            self._db = None

    async def enqueue_many(self, samples: list[dict[str, Any]]) -> int:
        assert self._db is not None
        now = datetime.now(UTC).isoformat()
        rows = [
            (
                str(uuid4()),
                s.get("ts", now),
                str(s["asset_id"]),
                s["metric"],
                float(s["value"]),
                s.get("unit"),
                json.dumps(s.get("tags") or {}),
            )
            for s in samples
        ]
        await self._db.executemany(
            "INSERT INTO samples (id, ts, asset_id, metric, value, unit, tags_json) VALUES (?, ?, ?, ?, ?, ?, ?)",
            rows,
        )
        await self._db.commit()
        return len(rows)

    async def drain_batch(self, max_size: int = 1000) -> tuple[list[str], list[dict[str, Any]]]:
        assert self._db is not None
        cur = await self._db.execute(
            "SELECT id, ts, asset_id, metric, value, unit, tags_json FROM samples ORDER BY ts ASC LIMIT ?",
            (max_size,),
        )
        rows = await cur.fetchall()
        ids = [r[0] for r in rows]
        samples = [
            {
                "asset_id": UUID(r[2]),
                "metric": r[3],
                "value": r[4],
                "unit": r[5],
                "ts": r[1],
                "tags": json.loads(r[6]) if r[6] else {},
            }
            for r in rows
        ]
        return ids, samples

    async def ack(self, ids: list[str]) -> None:
        assert self._db is not None
        if not ids:
            return
        placeholders = ",".join("?" * len(ids))
        await self._db.execute(f"DELETE FROM samples WHERE id IN ({placeholders})", ids)
        await self._db.commit()

    async def depth(self) -> int:
        assert self._db is not None
        cur = await self._db.execute("SELECT COUNT(*) FROM samples")
        row = await cur.fetchone()
        return int(row[0]) if row else 0
