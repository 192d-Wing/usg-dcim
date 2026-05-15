package main

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func mkBatch(samples []sample) *batch {
	return &batch{
		BatchID:     "batch-0001-xyz",
		SiteID:      uuid.New(),
		CollectorID: uuid.New(),
		Samples:     samples,
	}
}

func mkSample(metric string, value float64, tags map[string]string) sample {
	return sample{
		AssetID: uuid.New(),
		Metric:  metric,
		Value:   value,
		Unit:    "kW",
		Ts:      time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC),
		Tags:    tags,
	}
}

func TestHypertableRows_CountMatchesSamples(t *testing.T) {
	b := mkBatch([]sample{
		mkSample("m1", 1.0, nil),
		mkSample("m2", 2.0, nil),
		mkSample("m3", 3.0, nil),
	})
	rows, err := hypertableRows(b, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("row count = %d, want 3", len(rows))
	}
}

func TestHypertableRows_SeqIsDenseAndZeroIndexed(t *testing.T) {
	// seq is part of uq_telem_sample_dedup; retries must produce the
	// same (collector_id, batch_id, seq, ts) tuple for the same sample.
	b := mkBatch([]sample{
		mkSample("m1", 1.0, nil),
		mkSample("m2", 2.0, nil),
		mkSample("m3", 3.0, nil),
	})
	rows, _ := hypertableRows(b, time.Now())
	for i, row := range rows {
		// Row layout: [ts, site_id, asset_id, collector_id, batch_id, seq, ...]
		// seq is at position 5.
		got, ok := row[5].(int)
		if !ok {
			t.Fatalf("row %d seq is not int: %T", i, row[5])
		}
		if got != i {
			t.Errorf("row %d seq = %d, want %d", i, got, i)
		}
	}
}

func TestHypertableRows_TagsDefaultToEmptyJSON(t *testing.T) {
	// hypertable column has server_default '{}'::jsonb but the row builder
	// must give Postgres the empty object explicitly so the default fires
	// only on schema-evolution paths, not on the hot ingest path.
	b := mkBatch([]sample{mkSample("m1", 1.0, nil)})
	rows, _ := hypertableRows(b, time.Now())
	tags, ok := rows[0][10].([]byte)
	if !ok {
		t.Fatalf("tags column is not []byte: %T", rows[0][10])
	}
	if string(tags) != "{}" {
		t.Errorf("default tags JSON = %q, want %q", string(tags), "{}")
	}
}

func TestHypertableRows_TagsSerializedAsJSON(t *testing.T) {
	b := mkBatch([]sample{mkSample("m1", 1.0, map[string]string{"phase": "A"})})
	rows, _ := hypertableRows(b, time.Now())
	tags, ok := rows[0][10].([]byte)
	if !ok {
		t.Fatalf("tags column is not []byte: %T", rows[0][10])
	}
	if string(tags) != `{"phase":"A"}` {
		t.Errorf("tags JSON = %q, want %q", string(tags), `{"phase":"A"}`)
	}
}

func TestHypertableRows_UnitOmitsEmptyString(t *testing.T) {
	// Empty Unit should map to nil (NULL in Postgres), not the empty string,
	// so the column reads as NULL rather than '' in downstream queries.
	s := mkSample("m1", 1.0, nil)
	s.Unit = ""
	b := mkBatch([]sample{s})
	rows, _ := hypertableRows(b, time.Now())
	if rows[0][8] != nil {
		t.Errorf("empty unit should be nil, got %v (%T)", rows[0][8], rows[0][8])
	}
}

func TestHypertableRows_BatchMetadataPropagates(t *testing.T) {
	b := mkBatch([]sample{
		mkSample("m1", 1.0, nil),
		mkSample("m2", 2.0, nil),
	})
	rcv := time.Date(2026, 5, 15, 12, 5, 0, 0, time.UTC)
	rows, _ := hypertableRows(b, rcv)
	for i, row := range rows {
		if row[1] != b.SiteID {
			t.Errorf("row %d site_id mismatch", i)
		}
		if row[3] != b.CollectorID {
			t.Errorf("row %d collector_id mismatch", i)
		}
		if row[4] != b.BatchID {
			t.Errorf("row %d batch_id mismatch", i)
		}
		if row[9] != rcv {
			t.Errorf("row %d received_at mismatch", i)
		}
	}
}
