// Telemetry ingest service — Go port of backend/src/dcim/services/telemetry.py.
//
// Wire-compatible replacement for POST /v1/ingest/telemetry. Authentication
// uses the same `dcim_*` API tokens stored in api_tokens; the token must
// carry the `collectors:ingest:write` permission code (same rule the
// FastAPI deps enforce).
//
// Differences from the Python pipeline:
//   - Freshness upsert is a single INSERT … ON CONFLICT statement per
//     batch, not N selects + N updates.
//   - Hypertable insert + freshness upsert run concurrently on separate
//     PG connections.
//
// Telemetry samples land in the TimescaleDB `telemetry_samples` hypertable.
// OpenSearch was the original store but was retired after the read paths
// moved to the hypertable; this service no longer talks to OpenSearch.
// Set DCIM_TELEMETRY_WRITE_HYPERTABLE=false when running against a stock
// Postgres without TimescaleDB — the ingest endpoint still 200s and the
// freshness row still updates, but no sample is persisted.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/usg-dcim/packages/shared-go/env"
	"github.com/usg-dcim/packages/shared-go/promx"
)

// Counters/histograms exposed at /metrics. Service-namespaced under
// `dcim_heron_` so they don't collide with the Python otter's
// `dcim_telemetry_*` series during the cutover. Once heron is the sole
// ingest producer, the Python otter telemetry counters in
// packages/otter/src/dcim/metrics.py can be retired in favor of these.
var (
	ingestBatchesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dcim_heron_ingest_batches_total",
		Help: "Ingest batches processed by outcome (ok, hyper_error, fresh_error, auth_error, parse_error).",
	}, []string{"outcome"})

	ingestSamplesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "dcim_heron_ingest_samples_total",
		Help: "Total samples accepted by the ingest endpoint.",
	})

	ingestDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "dcim_heron_ingest_duration_seconds",
		Help:    "End-to-end duration of /v1/ingest/telemetry.",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0},
	})
)

const requiredCap = "collectors:ingest:write"

type sample struct {
	AssetID uuid.UUID         `json:"asset_id"`
	Metric  string            `json:"metric"`
	Value   float64           `json:"value"`
	Unit    string            `json:"unit,omitempty"`
	Ts      time.Time         `json:"ts"`
	Tags    map[string]string `json:"tags,omitempty"`
}

type batch struct {
	BatchID     string    `json:"batch_id"`
	SiteID      uuid.UUID `json:"site_id"`
	CollectorID uuid.UUID `json:"collector_id"`
	Samples     []sample  `json:"samples"`
}

type server struct {
	pg             *pgxpool.Pool
	log            *slog.Logger
	writeHypertable bool
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	pgDSN := env.String("DCIM_POSTGRES_DSN_RAW", "postgres://dcim:dcim@postgres:5432/dcim")
	addr := env.String("INGEST_ADDR", ":8100")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pg, err := pgxpool.New(ctx, pgDSN)
	if err != nil {
		log.Error("pg_connect_failed", "err", err)
		os.Exit(1)
	}
	defer pg.Close()

	// Opt-out for stock-PG deployments without the TimescaleDB extension.
	// Matches the Python settings.telemetry_write_hypertable flag.
	writeHypertable := env.Bool("DCIM_TELEMETRY_WRITE_HYPERTABLE", true)

	s := &server{
		pg:              pg,
		log:             log,
		writeHypertable: writeHypertable,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	promx.Mount(mux)
	// Path matches the Python API surface (FastAPI mounts at /api/v1).
	// /v1/ingest/telemetry is also accepted for clients that talk
	// directly to this service without going through the api proxy.
	mux.HandleFunc("/api/v1/ingest/telemetry", s.handleIngest)
	mux.HandleFunc("/v1/ingest/telemetry", s.handleIngest)

	srv := &http.Server{Addr: addr, Handler: mux}

	certFile := os.Getenv("INGEST_TLS_CERT")
	keyFile := os.Getenv("INGEST_TLS_KEY")
	if certFile != "" && keyFile != "" {
		tlsCfg, err := buildTLSConfig(
			os.Getenv("INGEST_TLS_CLIENT_CA"),
			env.Bool("INGEST_TLS_REQUIRE_CLIENT_CERT", false),
		)
		if err != nil {
			log.Error("tls_config_failed", "err", err)
			os.Exit(1)
		}
		srv.TLSConfig = tlsCfg
		log.Info("ingest_listen_tls", "addr", addr, "mtls", tlsCfg.ClientAuth != tls.NoClientCert)
		if err := srv.ListenAndServeTLS(certFile, keyFile); err != nil {
			log.Error("listen_failed", "err", err)
			os.Exit(1)
		}
		return
	}

	log.Info("ingest_listen", "addr", addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Error("listen_failed", "err", err)
		os.Exit(1)
	}
}

// buildTLSConfig returns a TLS config suitable for ListenAndServeTLS.
// If clientCAFile is set, client certs are accepted; if requireClientCert
// is true, a valid client cert is required and verified against the CA.
func buildTLSConfig(clientCAFile string, requireClientCert bool) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if clientCAFile == "" {
		return cfg, nil
	}
	pem, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, fmt.Errorf("read client CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no PEM certs found in %s", clientCAFile)
	}
	cfg.ClientCAs = pool
	if requireClientCert {
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	} else {
		cfg.ClientAuth = tls.VerifyClientCertIfGiven
	}
	return cfg, nil
}

func (s *server) handleIngest(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	defer func() { ingestDuration.Observe(time.Since(started).Seconds()) }()

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := s.authorize(r); err != nil {
		s.log.Warn("auth_rejected", "err", err)
		ingestBatchesTotal.WithLabelValues("auth_error").Inc()
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	var b batch
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		ingestBatchesTotal.WithLabelValues("parse_error").Inc()
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(b.Samples) == 0 || len(b.Samples) > 5000 {
		ingestBatchesTotal.WithLabelValues("parse_error").Inc()
		http.Error(w, "samples must be 1..5000", http.StatusBadRequest)
		return
	}
	if len(b.BatchID) < 8 || len(b.BatchID) > 64 {
		ingestBatchesTotal.WithLabelValues("parse_error").Inc()
		http.Error(w, "batch_id must be 8..64 chars", http.StatusBadRequest)
		return
	}

	receivedAt := time.Now().UTC()

	// Fan out hypertable insert + freshness upsert concurrently on
	// separate PG connections. Both errors are reported to the client —
	// the hypertable insert is no longer fail-open because it's the
	// primary store; if it fails we want the collector to retry.
	var wg sync.WaitGroup
	var hyperErr, freshErr error
	if s.writeHypertable {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hyperErr = s.writeHypertableRows(r.Context(), &b, receivedAt)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		freshErr = s.upsertFreshness(r.Context(), &b, receivedAt)
	}()
	wg.Wait()

	if hyperErr != nil {
		s.log.Error("hypertable_write_failed", "err", hyperErr, "batch", b.BatchID)
		ingestBatchesTotal.WithLabelValues("hyper_error").Inc()
		http.Error(w, "hypertable write failed", http.StatusBadGateway)
		return
	}
	if freshErr != nil {
		s.log.Error("freshness_upsert_failed", "err", freshErr, "batch", b.BatchID)
		ingestBatchesTotal.WithLabelValues("fresh_error").Inc()
		http.Error(w, "freshness upsert failed", http.StatusBadGateway)
		return
	}

	ingestBatchesTotal.WithLabelValues("ok").Inc()
	ingestSamplesTotal.Add(float64(len(b.Samples)))

	resp := map[string]any{
		"accepted":    len(b.Samples),
		"errors":      false,
		"received_at": receivedAt.Format(time.RFC3339Nano),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// authorize: parses Authorization: Bearer dcim_<token>, looks the digest
// up in api_tokens, and confirms the token's permission_codes column
// includes `collectors:ingest:write`. JWT-bearing principals are
// rejected — collectors are expected to use long-lived API tokens.
func (s *server) authorize(r *http.Request) error {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return fmt.Errorf("missing bearer")
	}
	raw := strings.TrimPrefix(h, "Bearer ")
	if !strings.HasPrefix(raw, "dcim_") {
		return fmt.Errorf("expected api token")
	}
	digestBytes := sha256.Sum256([]byte(raw))
	digest := hex.EncodeToString(digestBytes[:])

	var permsJSON []byte
	var revoked bool
	err := s.pg.QueryRow(r.Context(),
		`SELECT permission_codes, revoked FROM api_tokens WHERE token_hash = $1`,
		digest,
	).Scan(&permsJSON, &revoked)
	if err != nil {
		return fmt.Errorf("token not found")
	}
	if revoked {
		return fmt.Errorf("token revoked")
	}
	var codes []string
	if err := json.Unmarshal(permsJSON, &codes); err != nil {
		return fmt.Errorf("token perms malformed")
	}
	for _, c := range codes {
		if capabilityMatches(c, requiredCap) {
			return nil
		}
	}
	return fmt.Errorf("missing capability %s", requiredCap)
}

// Mirror of security.deps.find_matching_capability — wildcard glob
// over `:`-separated capability codes.
func capabilityMatches(held, want string) bool {
	if held == want || held == "*" {
		return true
	}
	hp := strings.Split(held, ":")
	wp := strings.Split(want, ":")
	if len(hp) != len(wp) {
		return false
	}
	for i := range hp {
		if hp[i] != "*" && hp[i] != wp[i] {
			return false
		}
	}
	return true
}

// writeHypertableRows inserts every sample in the batch into the
// `telemetry_samples` hypertable created by migration 0046. The unique
// constraint `uq_telem_sample_dedup` on (collector_id, batch_id, seq, ts)
// makes collector retries idempotent — ON CONFLICT DO NOTHING drops the
// already-written row instead of erroring.
func (s *server) writeHypertableRows(ctx context.Context, b *batch, receivedAt time.Time) error {
	if len(b.Samples) == 0 {
		return nil
	}
	rows, err := hypertableRows(b, receivedAt)
	if err != nil {
		return err
	}

	// Multi-row INSERT with ON CONFLICT DO NOTHING. pgx's CopyFrom is
	// faster but doesn't support ON CONFLICT, and idempotence matters
	// more here than throughput.
	var sb strings.Builder
	sb.WriteString(`INSERT INTO telemetry_samples
		(ts, site_id, asset_id, collector_id, batch_id, seq, metric,
		 value, unit, received_at, tags) VALUES `)
	args := make([]any, 0, len(rows)*11)
	for i, row := range rows {
		if i > 0 {
			sb.WriteString(", ")
		}
		base := i * 11
		fmt.Fprintf(&sb, "($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
			base+1, base+2, base+3, base+4, base+5, base+6,
			base+7, base+8, base+9, base+10, base+11)
		args = append(args, row...)
	}
	sb.WriteString(" ON CONFLICT ON CONSTRAINT uq_telem_sample_dedup DO NOTHING")

	_, err = s.pg.Exec(ctx, sb.String(), args...)
	return err
}

// hypertableRows is the pure row-builder for writeHypertableRows. Extracted
// so a unit test can lock the per-row argument order, seq numbering, and
// tags-default-to-empty-JSON behavior without touching the database.
func hypertableRows(b *batch, receivedAt time.Time) ([][]any, error) {
	rows := make([][]any, 0, len(b.Samples))
	for i, sm := range b.Samples {
		var unit any
		if sm.Unit != "" {
			unit = sm.Unit
		}
		tagsJSON := []byte("{}")
		if len(sm.Tags) > 0 {
			j, err := json.Marshal(sm.Tags)
			if err != nil {
				return nil, fmt.Errorf("marshal tags: %w", err)
			}
			tagsJSON = j
		}
		rows = append(rows, []any{
			sm.Ts, b.SiteID, sm.AssetID, b.CollectorID,
			b.BatchID, i, sm.Metric, sm.Value, unit,
			receivedAt, tagsJSON,
		})
	}
	return rows, nil
}

// upsertFreshness: one statement per (asset_id, metric) pair in the batch.
// Uses INSERT … ON CONFLICT (asset_id, metric) DO UPDATE to fold the
// freshness sentinel row in a single round-trip per pair. Dedupes on the
// client side so a batch with many samples for the same asset/metric
// only writes the latest.
func (s *server) upsertFreshness(ctx context.Context, b *batch, receivedAt time.Time) error {
	type key struct {
		asset uuid.UUID
		met   string
	}
	latest := make(map[key]sample, len(b.Samples))
	for _, sm := range b.Samples {
		latest[key{sm.AssetID, sm.Metric}] = sm
	}

	tx, err := s.pg.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	const q = `
		INSERT INTO telemetry_sources
			(id, site_id, asset_id, collector_id, metric, unit,
			 last_success_at, last_reading_at, last_value, freshness,
			 poll_interval_seconds, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, 'current', 60, NOW(), NOW())
		ON CONFLICT (asset_id, metric) DO UPDATE SET
			collector_id    = EXCLUDED.collector_id,
			unit            = COALESCE(EXCLUDED.unit, telemetry_sources.unit),
			last_success_at = EXCLUDED.last_success_at,
			last_reading_at = EXCLUDED.last_reading_at,
			last_value      = EXCLUDED.last_value,
			freshness       = 'current',
			updated_at      = NOW()
	`
	for k, sm := range latest {
		unit := any(nil)
		if sm.Unit != "" {
			unit = sm.Unit
		}
		if _, err := tx.Exec(ctx, q,
			b.SiteID, k.asset, b.CollectorID, k.met, unit,
			receivedAt, sm.Ts, sm.Value,
		); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Convert PG bytea / int returns if needed.
var _ = strconv.Itoa
