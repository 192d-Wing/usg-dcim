"""Elasticsearch client + index helpers for telemetry/events/rollups."""

from __future__ import annotations

from datetime import datetime
from functools import lru_cache

from elasticsearch import AsyncElasticsearch

from ..settings import get_settings


@lru_cache
def client() -> AsyncElasticsearch:
    s = get_settings()
    auth = (s.elastic_username, s.elastic_password) if s.elastic_username else None
    return AsyncElasticsearch(s.elastic_url, basic_auth=auth)


def telemetry_index(site_id: str, ts: datetime | None = None) -> str:
    s = get_settings()
    when = ts or datetime.utcnow()
    return f"{s.telemetry_index_prefix}-{site_id}-{when:%Y-%m}"


def events_index(ts: datetime | None = None) -> str:
    s = get_settings()
    when = ts or datetime.utcnow()
    return f"{s.events_index_prefix}-{when:%Y-%m}"


def rollup_index(grain: str, ts: datetime | None = None) -> str:
    """grain in {hourly, daily}."""
    s = get_settings()
    when = ts or datetime.utcnow()
    return f"{s.rollup_index_prefix}-{grain}-{when:%Y}"


TELEMETRY_MAPPING: dict = {
    "mappings": {
        "properties": {
            "site_id": {"type": "keyword"},
            "collector_id": {"type": "keyword"},
            "asset_id": {"type": "keyword"},
            "metric": {"type": "keyword"},
            "value": {"type": "double"},
            "unit": {"type": "keyword"},
            "ts": {"type": "date"},
            "received_at": {"type": "date"},
            "tags": {"type": "object", "dynamic": True},
        }
    },
    "settings": {"index": {"refresh_interval": "5s", "number_of_shards": 1, "number_of_replicas": 1}},
}


async def ensure_index(name: str) -> None:
    es = client()
    if not await es.indices.exists(index=name):
        await es.indices.create(index=name, body=TELEMETRY_MAPPING)
