//go:build integration

// Live-DB verification of dualWriteHypertable. Run with:
//
//   DCIM_PG_TEST_DSN="postgres://dcim:dcim@localhost:5432/dcim" \
//     go test -tags=integration -run TestDualWriteHypertable_Live -v
//
// Not part of the default `go test ./...` run — it needs a real Postgres
// with migration 0046 applied.
package main

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDualWriteHypertable_Live(t *testing.T) {
	dsn := os.Getenv("DCIM_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("DCIM_PG_TEST_DSN not set")
	}
	ctx := context.Background()
	pg, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pg connect: %v", err)
	}
	defer pg.Close()

	s := &server{pg: pg, log: slog.New(slog.NewTextHandler(os.Stderr, nil))}

	// Seed Region+Site+Asset+Collector so the hypertable's FK constraints
	// resolve. Re-uses existing rows if a prior run left fixtures behind.
	siteID, assetID, collectorID := mustEnsureFixtures(t, ctx, pg)
	b := &batch{
		BatchID:     "go-verify-batch-001",
		SiteID:      siteID,
		CollectorID: collectorID,
		Samples: []sample{
			{AssetID: assetID, Metric: "pdu.input.kw", Value: 1.0, Unit: "kW", Ts: time.Now().UTC()},
			{AssetID: assetID, Metric: "pdu.input.kw", Value: 2.0, Unit: "kW", Ts: time.Now().UTC().Add(-time.Second)},
		},
	}
	recv := time.Now().UTC()

	// Step 1: insert.
	var before int
	if err := pg.QueryRow(ctx,
		`SELECT COUNT(*) FROM telemetry_samples WHERE batch_id = $1`,
		b.BatchID,
	).Scan(&before); err != nil {
		t.Fatalf("count before: %v", err)
	}
	if err := s.dualWriteHypertable(ctx, b, recv); err != nil {
		t.Fatalf("dualWriteHypertable: %v", err)
	}
	var after int
	if err := pg.QueryRow(ctx,
		`SELECT COUNT(*) FROM telemetry_samples WHERE batch_id = $1`,
		b.BatchID,
	).Scan(&after); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if after-before != len(b.Samples) {
		t.Errorf("step 1: rows %d -> %d, expected +%d", before, after, len(b.Samples))
	}

	// Step 2: re-insert same batch — ON CONFLICT DO NOTHING should not grow the count.
	if err := s.dualWriteHypertable(ctx, b, recv); err != nil {
		t.Fatalf("dualWriteHypertable retry: %v", err)
	}
	var afterRetry int
	if err := pg.QueryRow(ctx,
		`SELECT COUNT(*) FROM telemetry_samples WHERE batch_id = $1`,
		b.BatchID,
	).Scan(&afterRetry); err != nil {
		t.Fatalf("count after retry: %v", err)
	}
	if afterRetry != after {
		t.Errorf("step 2: rows %d -> %d, expected unchanged", after, afterRetry)
	}

	// Cleanup so subsequent runs start fresh.
	_, _ = pg.Exec(ctx,
		`DELETE FROM telemetry_samples WHERE batch_id = $1`, b.BatchID)

	t.Logf("OK — step 1: +%d rows; step 2: idempotent (count unchanged)", len(b.Samples))
}

func mustEnsureFixtures(t *testing.T, ctx context.Context, pg *pgxpool.Pool) (siteID, assetID, collectorID uuid.UUID) {
	t.Helper()
	// region
	var regionID uuid.UUID
	err := pg.QueryRow(ctx, `SELECT id FROM regions WHERE code = 'GVR' LIMIT 1`).Scan(&regionID)
	if err != nil {
		regionID = uuid.New()
		if _, err := pg.Exec(ctx,
			`INSERT INTO regions (id, name, code, created_at, updated_at)
			 VALUES ($1, 'go-verify-region', 'GVR', NOW(), NOW())`,
			regionID,
		); err != nil {
			t.Fatalf("seed region: %v", err)
		}
	}
	// site
	err = pg.QueryRow(ctx, `SELECT id FROM sites WHERE code = 'GVS01' LIMIT 1`).Scan(&siteID)
	if err != nil {
		siteID = uuid.New()
		if _, err := pg.Exec(ctx,
			`INSERT INTO sites (id, region_id, name, code, lifecycle_state, metadata_json, created_at, updated_at)
			 VALUES ($1, $2, 'go-verify-site', 'GVS01', 'active', '{}'::jsonb, NOW(), NOW())`,
			siteID, regionID,
		); err != nil {
			t.Fatalf("seed site: %v", err)
		}
	}
	// asset
	err = pg.QueryRow(ctx, `SELECT id FROM assets WHERE site_id = $1 LIMIT 1`, siteID).Scan(&assetID)
	if err != nil {
		assetID = uuid.New()
		if _, err := pg.Exec(ctx,
			`INSERT INTO assets (id, site_id, name, kind, face, mount, lifecycle_state, metadata_json, created_at, updated_at)
			 VALUES ($1, $2, 'go-verify-pdu', 'pdu', 'front', 'rack', 'active', '{}'::jsonb, NOW(), NOW())`,
			assetID, siteID,
		); err != nil {
			t.Fatalf("seed asset: %v", err)
		}
	}
	// collector
	err = pg.QueryRow(ctx, `SELECT id FROM collectors WHERE site_id = $1 LIMIT 1`, siteID).Scan(&collectorID)
	if err != nil {
		collectorID = uuid.New()
		if _, err := pg.Exec(ctx,
			`INSERT INTO collectors (id, site_id, name, status, capabilities, buffered_samples, enabled, config_overrides, created_at, updated_at)
			 VALUES ($1, $2, 'go-verify-collector', 'pending', '[]'::json, 0, true, '{}'::jsonb, NOW(), NOW())`,
			collectorID, siteID,
		); err != nil {
			t.Fatalf("seed collector: %v", err)
		}
	}
	return siteID, assetID, collectorID
}
