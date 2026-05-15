"""Unit tests for the OpenSearch → TimescaleDB dual-write row builder.

The full ingest path hits OpenSearch + Postgres; this module covers the
pure row-shape helper so we can lock the hypertable contract (column set,
seq numbering, dedup-key fields) without standing up either backend.
"""

from datetime import UTC, datetime
from uuid import uuid4

from pytest import approx

from dcim.schemas.telemetry import TelemetryBatch, TelemetrySample
from dcim.services.telemetry import _hypertable_rows, _opensearch_actions


def _sample(metric: str = "pdu.input.kw", value: float = 1.0, ts: datetime | None = None):
    return TelemetrySample(
        asset_id=uuid4(),
        metric=metric,
        value=value,
        unit="kW",
        ts=ts or datetime(2026, 5, 15, 12, 0, 0, tzinfo=UTC),
        tags={"phase": "A"},
    )


def _batch(samples):
    return TelemetryBatch(
        batch_id="batch-0001-xyz",
        site_id=uuid4(),
        collector_id=uuid4(),
        samples=samples,
    )


def test_row_count_matches_sample_count():
    b = _batch([_sample(), _sample(), _sample()])
    rows = _hypertable_rows(b, datetime.now(UTC))
    assert len(rows) == 3


def test_seq_is_dense_and_zero_indexed():
    """seq is part of the dedup unique constraint; retries must produce the
    same (collector_id, batch_id, seq, ts) tuple for the same sample."""
    b = _batch([_sample(), _sample(), _sample()])
    rows = _hypertable_rows(b, datetime.now(UTC))
    assert [r["seq"] for r in rows] == [0, 1, 2]


def test_dedup_key_fields_match_unique_constraint():
    """Locks the column set in `uq_telem_sample_dedup` against the row builder.
    If migration 0046's constraint changes, this test should change too."""
    b = _batch([_sample()])
    row = _hypertable_rows(b, datetime.now(UTC))[0]
    for key in ("collector_id", "batch_id", "seq", "ts"):
        assert key in row, f"dedup key field {key!r} missing from row"


def test_batch_metadata_propagates_to_every_row():
    b = _batch([_sample(metric="m1"), _sample(metric="m2")])
    received = datetime(2026, 5, 15, 12, 5, 0, tzinfo=UTC)
    rows = _hypertable_rows(b, received)
    for r in rows:
        assert r["site_id"] == b.site_id
        assert r["collector_id"] == b.collector_id
        assert r["batch_id"] == b.batch_id
        assert r["received_at"] == received


def test_per_sample_fields_propagate_distinctly():
    s1 = _sample(metric="m1", value=1.5)
    s2 = _sample(metric="m2", value=2.5)
    rows = _hypertable_rows(_batch([s1, s2]), datetime.now(UTC))
    assert rows[0]["metric"] == "m1"
    assert rows[0]["value"] == approx(1.5)
    assert rows[0]["asset_id"] == s1.asset_id
    assert rows[1]["metric"] == "m2"
    assert rows[1]["value"] == approx(2.5)
    assert rows[1]["asset_id"] == s2.asset_id


def test_tags_default_to_empty_dict_not_none():
    """The hypertable column has `nullable=False, server_default="'{}'"` —
    the row builder must give Postgres a dict, not None, so the default
    fires only on schema-evolution paths and not on the hot ingest path."""
    s = TelemetrySample(
        asset_id=uuid4(), metric="m", value=1.0, unit=None,
        ts=datetime.now(UTC),
    )
    rows = _hypertable_rows(_batch([s]), datetime.now(UTC))
    assert rows[0]["tags"] == {}


# --- _opensearch_actions (NDJSON action builder) -------------------------

def test_opensearch_action_count_equals_sample_count():
    """One dict per document — not the alternating action/source pairs of
    the elasticsearch-py bulk format. The 2x-doc bug came from feeding
    those pairs to async_bulk as if each pair element were its own doc."""
    b = _batch([_sample(), _sample(), _sample()])
    actions = _opensearch_actions("idx", b, datetime.now(UTC))
    assert len(actions) == 3


def test_opensearch_action_carries_index_id_and_source():
    """async_bulk expects `_index`, `_id`, `_source` on each action dict."""
    b = _batch([_sample()])
    a = _opensearch_actions("dcim-telemetry-x-2026-05", b, datetime.now(UTC))[0]
    assert a["_index"] == "dcim-telemetry-x-2026-05"
    assert a.get("_id")
    assert isinstance(a.get("_source"), dict)


def test_opensearch_action_id_is_stable_across_calls():
    """Idempotency on collector retries depends on the doc id being a
    deterministic function of (collector_id, batch_id, seq)."""
    b = _batch([_sample(), _sample()])
    now = datetime.now(UTC)
    ids_first = [a["_id"] for a in _opensearch_actions("idx", b, now)]
    ids_second = [a["_id"] for a in _opensearch_actions("idx", b, now)]
    assert ids_first == ids_second
    assert ids_first[0] != ids_first[1]  # seq differentiates them
