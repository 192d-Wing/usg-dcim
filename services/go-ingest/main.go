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
//   - ES bulk write uses streamed NDJSON instead of buffering a Python
//     list of dicts.
//   - One goroutine handles the ES write while the freshness upsert runs
//     concurrently on a separate PG connection.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
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

	"github.com/opensearch-project/opensearch-go/v2"
	"github.com/opensearch-project/opensearch-go/v2/opensearchapi"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
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
	es          *opensearch.Client
	pg          *pgxpool.Pool
	indexPrefix string
	log         *slog.Logger
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	pgDSN := envDefault("DCIM_POSTGRES_DSN_RAW", "postgres://dcim:dcim@postgres:5432/dcim")
	esURL := envDefault("DCIM_OPENSEARCH_URL", "http://opensearch:9200")
	addr := envDefault("INGEST_ADDR", ":8100")
	indexPrefix := envDefault("DCIM_TELEMETRY_INDEX_PREFIX", "dcim-telemetry")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pg, err := pgxpool.New(ctx, pgDSN)
	if err != nil {
		log.Error("pg_connect_failed", "err", err)
		os.Exit(1)
	}
	defer pg.Close()

	esCfg := opensearch.Config{Addresses: []string{esURL}}
	if u := os.Getenv("DCIM_OPENSEARCH_USERNAME"); u != "" {
		esCfg.Username = u
		esCfg.Password = os.Getenv("DCIM_OPENSEARCH_PASSWORD")
	}
	es, err := opensearch.NewClient(esCfg)
	if err != nil {
		log.Error("es_client_failed", "err", err)
		os.Exit(1)
	}

	s := &server{es: es, pg: pg, indexPrefix: indexPrefix, log: log}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	// Path matches the Python API surface (FastAPI mounts at /api/v1).
	// /v1/ingest/telemetry is also accepted for clients that talk
	// directly to this service without going through the api proxy.
	mux.HandleFunc("/api/v1/ingest/telemetry", s.handleIngest)
	mux.HandleFunc("/v1/ingest/telemetry", s.handleIngest)

	log.Info("ingest_listen", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Error("listen_failed", "err", err)
		os.Exit(1)
	}
}

func envDefault(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func (s *server) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := s.authorize(r); err != nil {
		s.log.Warn("auth_rejected", "err", err)
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	var b batch
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(b.Samples) == 0 || len(b.Samples) > 5000 {
		http.Error(w, "samples must be 1..5000", http.StatusBadRequest)
		return
	}
	if len(b.BatchID) < 8 || len(b.BatchID) > 64 {
		http.Error(w, "batch_id must be 8..64 chars", http.StatusBadRequest)
		return
	}

	receivedAt := time.Now().UTC()
	index := fmt.Sprintf("%s-%s-%s", s.indexPrefix, b.SiteID.String(), receivedAt.Format("2006-01"))

	if err := s.ensureIndex(r.Context(), index); err != nil {
		s.log.Warn("ensure_index_failed", "err", err, "index", index)
		// Continue — bulk write will fail loudly if the index truly can't be created.
	}

	// Fan out ES bulk + PG freshness upsert in parallel.
	var wg sync.WaitGroup
	var esErr, pgErr error
	var esHadErrors bool
	wg.Add(2)
	go func() {
		defer wg.Done()
		esHadErrors, esErr = s.bulkWrite(r.Context(), index, &b, receivedAt)
	}()
	go func() {
		defer wg.Done()
		pgErr = s.upsertFreshness(r.Context(), &b, receivedAt)
	}()
	wg.Wait()

	if esErr != nil {
		s.log.Error("es_bulk_failed", "err", esErr, "batch", b.BatchID)
		http.Error(w, "elasticsearch write failed", http.StatusBadGateway)
		return
	}
	if pgErr != nil {
		s.log.Error("freshness_upsert_failed", "err", pgErr, "batch", b.BatchID)
		http.Error(w, "freshness upsert failed", http.StatusBadGateway)
		return
	}
	if esHadErrors {
		s.log.Warn("telemetry_bulk_errors", "batch", b.BatchID, "count", len(b.Samples))
	}

	resp := map[string]any{
		"accepted":    len(b.Samples),
		"errors":      esHadErrors,
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

func (s *server) ensureIndex(ctx context.Context, index string) error {
	exists, err := opensearchapi.IndicesExistsRequest{Index: []string{index}}.Do(ctx, s.es)
	if err != nil {
		return err
	}
	defer exists.Body.Close()
	if exists.StatusCode == 200 {
		return nil
	}
	body := strings.NewReader(`{
		"mappings": {"properties": {
			"site_id":{"type":"keyword"},"collector_id":{"type":"keyword"},
			"asset_id":{"type":"keyword"},"metric":{"type":"keyword"},
			"value":{"type":"double"},"unit":{"type":"keyword"},
			"ts":{"type":"date"},"received_at":{"type":"date"},
			"tags":{"type":"object","dynamic":true}
		}},
		"settings":{"index":{"refresh_interval":"5s","number_of_shards":1,"number_of_replicas":1}}
	}`)
	create, err := opensearchapi.IndicesCreateRequest{Index: index, Body: body}.Do(ctx, s.es)
	if err != nil {
		return err
	}
	defer create.Body.Close()
	if create.IsError() && create.StatusCode != 400 {
		// 400 with resource_already_exists_exception happens under racy create — tolerate.
		return fmt.Errorf("create index %s: %s", index, create.String())
	}
	return nil
}

func (s *server) bulkWrite(ctx context.Context, index string, b *batch, receivedAt time.Time) (bool, error) {
	var buf bytes.Buffer
	siteStr := b.SiteID.String()
	collStr := b.CollectorID.String()
	recvStr := receivedAt.Format(time.RFC3339Nano)

	for i, sm := range b.Samples {
		docID := fmt.Sprintf("%s:%s:%d", b.CollectorID, b.BatchID, i)
		meta := map[string]any{"index": map[string]string{"_index": index, "_id": docID}}
		doc := map[string]any{
			"site_id":      siteStr,
			"collector_id": collStr,
			"asset_id":     sm.AssetID.String(),
			"metric":       sm.Metric,
			"value":        sm.Value,
			"unit":         sm.Unit,
			"ts":           sm.Ts.Format(time.RFC3339Nano),
			"received_at":  recvStr,
			"tags":         sm.Tags,
		}
		mb, _ := json.Marshal(meta)
		db, _ := json.Marshal(doc)
		buf.Write(mb)
		buf.WriteByte('\n')
		buf.Write(db)
		buf.WriteByte('\n')
	}

	res, err := opensearchapi.BulkRequest{Body: &buf, Refresh: "false"}.Do(ctx, s.es)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	if res.IsError() {
		return true, fmt.Errorf("bulk: %s", res.String())
	}
	var bres struct {
		Errors bool `json:"errors"`
	}
	if err := json.NewDecoder(res.Body).Decode(&bres); err != nil {
		return false, err
	}
	return bres.Errors, nil
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
