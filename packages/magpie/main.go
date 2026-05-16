// Alert evaluator + collector sweep — Go port of
// backend/src/dcim/services/alerts.py.
//
// Two cron loops:
//
//	evaluate_rules   every 30s   — runs one SQL query per enabled rule
//	                              against the telemetry_samples hypertable,
//	                              fires/resolves alerts, enqueues notify jobs.
//	sweep_collectors every 30s   — checks collectors.last_seen_at and
//	                              synthesizes collector-down alerts.
//
// The Python notifier (notif_svc.dispatch_fire / dispatch_resolve) keeps
// living in the FastAPI worker. We re-enqueue into the same ARQ Redis
// queue so the actual email/webhook delivery stays in one place; here we
// only own evaluation + dedup + state.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/redis/go-redis/v9"

	"github.com/usg-dcim/packages/shared-go/env"
	"github.com/usg-dcim/packages/shared-go/promx"
)

// Metrics — service-namespaced under `dcim_magpie_` to avoid colliding
// with the Python otter's `dcim_alerts_*` series during cutover. Drop
// the Python emission in packages/otter/src/dcim/services/alerts.py once
// magpie is the sole producer.
var (
	alertEvalRunsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dcim_magpie_alert_eval_runs_total",
		Help: "Rule-evaluation runs by outcome (ok, load_error).",
	}, []string{"outcome"})

	ruleEvalsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dcim_magpie_rule_evals_total",
		Help: "Per-rule evaluations by outcome (ok, error, skipped).",
	}, []string{"outcome"})

	alertsFiredTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dcim_magpie_alerts_fired_total",
		Help: "Alerts transitioned to firing, by severity.",
	}, []string{"severity"})

	alertsResolvedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "dcim_magpie_alerts_resolved_total",
		Help: "Alerts transitioned to resolved.",
	})

	sweepRunsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "dcim_magpie_sweep_runs_total",
		Help: "Collector-sweep runs by outcome (ok, error).",
	}, []string{"outcome"})
)

type alertRule struct {
	ID              uuid.UUID
	Name            string
	Metric          string
	Operator        string
	Threshold       float64
	DurationSeconds int
	Severity        string
	SiteScopeID     *uuid.UUID
	Description     *string
}

type config struct {
	pgDSN              string
	redisURL           string
	evalInterval       time.Duration
	sweepInterval      time.Duration
	collectorStaleSecs int
	notifyQueueName    string
	maxConcurrentRules int
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := config{
		pgDSN:              env.String("DCIM_POSTGRES_DSN_RAW", "postgres://dcim:dcim@postgres:5432/dcim"),
		redisURL:           env.String("DCIM_REDIS_DSN", "redis://redis:6379/0"),
		evalInterval:       env.Duration("ALERTS_EVAL_INTERVAL", 30*time.Second),
		sweepInterval:      env.Duration("ALERTS_SWEEP_INTERVAL", 30*time.Second),
		collectorStaleSecs: env.Int("DCIM_COLLECTOR_STALE_SECONDS", 600),
		notifyQueueName:    env.String("ALERTS_NOTIFY_QUEUE", "arq:queue"),
		maxConcurrentRules: env.Int("ALERTS_MAX_PARALLEL", 16),
	}

	pg, err := pgxpool.New(context.Background(), cfg.pgDSN)
	if err != nil {
		log.Error("pg_connect_failed", "err", err)
		os.Exit(1)
	}
	defer pg.Close()

	rOpts, err := redis.ParseURL(cfg.redisURL)
	if err != nil {
		log.Error("redis_parse_failed", "err", err)
		os.Exit(1)
	}
	rdb := redis.NewClient(rOpts)
	defer rdb.Close()

	e := &engine{pg: pg, rdb: rdb, cfg: cfg, log: log}

	// Healthz on a side port so kubelet probes work.
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
		promx.Mount(mux)
		_ = http.ListenAndServe(env.String("ALERTS_HEALTH_ADDR", ":8101"), mux)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go tick(ctx, cfg.evalInterval, e.evaluateRules)
	go tick(ctx, cfg.sweepInterval, e.sweepCollectors)

	log.Info("alerts_running",
		"eval_every", cfg.evalInterval.String(),
		"sweep_every", cfg.sweepInterval.String(),
	)
	select {}
}

type engine struct {
	pg  *pgxpool.Pool
	rdb *redis.Client
	cfg config
	log *slog.Logger
}

func tick(ctx context.Context, d time.Duration, fn func(context.Context)) {
	fn(ctx)
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn(ctx)
		}
	}
}

var opCmp = map[string]func(a, b float64) bool{
	">":  func(a, b float64) bool { return a > b },
	">=": func(a, b float64) bool { return a >= b },
	"<":  func(a, b float64) bool { return a < b },
	"<=": func(a, b float64) bool { return a <= b },
	"==": func(a, b float64) bool { return a == b },
	"!=": func(a, b float64) bool { return a != b },
}

func (e *engine) evaluateRules(ctx context.Context) {
	rules, err := e.loadRules(ctx)
	if err != nil {
		e.log.Error("rules_load_failed", "err", err)
		alertEvalRunsTotal.WithLabelValues("load_error").Inc()
		return
	}
	alertEvalRunsTotal.WithLabelValues("ok").Inc()

	sem := make(chan struct{}, e.cfg.maxConcurrentRules)
	var wg sync.WaitGroup
	var mu sync.Mutex
	totalFired, totalResolved := 0, 0
	firedAlerts := []uuid.UUID{}
	resolvedAlerts := []uuid.UUID{}

	for _, r := range rules {
		if _, ok := opCmp[r.Operator]; !ok {
			continue
		}
		r := r
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			fired, resolved, fIDs, rIDs, err := e.evalOne(ctx, r)
			if err != nil {
				e.log.Warn("rule_eval_failed", "rule", r.Name, "err", err)
				ruleEvalsTotal.WithLabelValues("error").Inc()
				return
			}
			ruleEvalsTotal.WithLabelValues("ok").Inc()
			if fired > 0 {
				alertsFiredTotal.WithLabelValues(r.Severity).Add(float64(fired))
			}
			if resolved > 0 {
				alertsResolvedTotal.Add(float64(resolved))
			}
			mu.Lock()
			totalFired += fired
			totalResolved += resolved
			firedAlerts = append(firedAlerts, fIDs...)
			resolvedAlerts = append(resolvedAlerts, rIDs...)
			mu.Unlock()
		}()
	}
	wg.Wait()

	for _, id := range firedAlerts {
		e.enqueueNotify(ctx, "fire", id)
	}
	for _, id := range resolvedAlerts {
		e.enqueueNotify(ctx, "resolve", id)
	}

	e.log.Info("alerts_evaluated",
		"rules", len(rules), "fired", totalFired, "resolved", totalResolved,
	)
}

func (e *engine) loadRules(ctx context.Context) ([]alertRule, error) {
	rows, err := e.pg.Query(ctx, `
		SELECT id, name, metric, operator, threshold, duration_seconds,
		       severity::text, site_scope_id, description
		FROM alert_rules
		WHERE enabled = TRUE
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []alertRule
	for rows.Next() {
		var r alertRule
		if err := rows.Scan(&r.ID, &r.Name, &r.Metric, &r.Operator, &r.Threshold,
			&r.DurationSeconds, &r.Severity, &r.SiteScopeID, &r.Description); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (e *engine) evalOne(ctx context.Context, r alertRule) (int, int, []uuid.UUID, []uuid.UUID, error) {
	since := time.Now().UTC().Add(-time.Duration(r.DurationSeconds) * time.Second)

	// MAX(value) per asset over the rule's metric+window. Mirrors the
	// SQL the Python evaluator runs after step 2b; pgx maps *uuid.UUID
	// to a nullable UUID parameter, so site_scope_id NULL means
	// enterprise-wide.
	hyperRows, err := e.pg.Query(ctx, `
		SELECT asset_id, MAX(value) AS value
		FROM telemetry_samples
		WHERE metric = $1
		  AND ts >= $2
		  AND ($3::uuid IS NULL OR site_id = $3::uuid)
		GROUP BY asset_id
	`, r.Metric, since, r.SiteScopeID)
	if err != nil {
		return 0, 0, nil, nil, err
	}
	type assetReading struct {
		assetID string
		value   float64
	}
	var readings []assetReading
	for hyperRows.Next() {
		var ar assetReading
		if err := hyperRows.Scan(&ar.assetID, &ar.value); err != nil {
			hyperRows.Close()
			return 0, 0, nil, nil, err
		}
		readings = append(readings, ar)
	}
	hyperRows.Close()
	if err := hyperRows.Err(); err != nil {
		return 0, 0, nil, nil, err
	}

	cmp := opCmp[r.Operator]
	fired, resolved := 0, 0
	firedIDs, resolvedIDs := []uuid.UUID{}, []uuid.UUID{}

	now := time.Now().UTC()
	tx, err := e.pg.Begin(ctx)
	if err != nil {
		return 0, 0, nil, nil, err
	}
	defer tx.Rollback(ctx)

	for _, b := range readings {
		v := b.value
		violates := cmp(v, r.Threshold)
		dedupe := fmt.Sprintf("%s|%s|%s", r.ID, b.assetID, r.Metric)

		var existingID uuid.UUID
		err := tx.QueryRow(ctx,
			`SELECT id FROM alerts WHERE dedupe_key=$1 AND state='firing' LIMIT 1`,
			dedupe,
		).Scan(&existingID)
		hasExisting := err == nil

		switch {
		case violates && !hasExisting:
			if r.SiteScopeID == nil {
				continue
			}
			suppressed, err := e.isSuppressed(ctx, *r.SiteScopeID, now)
			if err != nil {
				return 0, 0, nil, nil, err
			}
			if suppressed {
				continue
			}
			id := uuid.New()
			labels, _ := json.Marshal(map[string]string{"metric": r.Metric, "rule": r.Name})
			summary := fmt.Sprintf("%s %s %g (got %.2f)", r.Metric, r.Operator, r.Threshold, v)
			detail := ""
			if r.Description != nil {
				detail = *r.Description
			}
			_, err = tx.Exec(ctx, `
				INSERT INTO alerts (id, rule_id, site_id, asset_id, severity, state,
					dedupe_key, summary, detail, first_seen_at, last_seen_at, labels_json,
					created_at, updated_at)
				VALUES ($1,$2,$3,$4,$5::alert_severity,'firing',$6,$7,$8,$9,$9,$10,NOW(),NOW())
			`, id, r.ID, r.SiteScopeID, b.assetID, r.Severity, dedupe, summary, detail, now, labels)
			if err != nil {
				return 0, 0, nil, nil, err
			}
			fired++
			firedIDs = append(firedIDs, id)
		case violates && hasExisting:
			if _, err := tx.Exec(ctx,
				`UPDATE alerts SET last_seen_at=$1, updated_at=NOW() WHERE id=$2`,
				now, existingID); err != nil {
				return 0, 0, nil, nil, err
			}
		case !violates && hasExisting:
			if _, err := tx.Exec(ctx,
				`UPDATE alerts SET state='resolved', resolved_at=$1, updated_at=NOW() WHERE id=$2`,
				now, existingID); err != nil {
				return 0, 0, nil, nil, err
			}
			resolved++
			resolvedIDs = append(resolvedIDs, existingID)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, nil, nil, err
	}
	return fired, resolved, firedIDs, resolvedIDs, nil
}

func (e *engine) isSuppressed(ctx context.Context, siteID uuid.UUID, now time.Time) (bool, error) {
	var one int
	err := e.pg.QueryRow(ctx, `
		SELECT 1 FROM maintenance_windows
		WHERE site_id=$1 AND starts_at <= $2 AND ends_at >= $2 LIMIT 1
	`, siteID, now).Scan(&one)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (e *engine) sweepCollectors(ctx context.Context) {
	now := time.Now().UTC()
	threshold := now.Add(-time.Duration(e.cfg.collectorStaleSecs) * time.Second)

	rows, err := e.pg.Query(ctx, `
		SELECT id, site_id, name, last_seen_at
		FROM collectors
		WHERE enabled = TRUE
		  AND last_seen_at IS NOT NULL
		  AND last_seen_at < $1
	`, threshold)
	if err != nil {
		e.log.Error("collector_sweep_failed", "err", err)
		sweepRunsTotal.WithLabelValues("error").Inc()
		return
	}
	defer rows.Close()
	sweepRunsTotal.WithLabelValues("ok").Inc()

	type stale struct {
		id, siteID uuid.UUID
		name       string
		lastSeen   time.Time
	}
	var ss []stale
	for rows.Next() {
		var s stale
		if err := rows.Scan(&s.id, &s.siteID, &s.name, &s.lastSeen); err != nil {
			e.log.Error("collector_scan_failed", "err", err)
			return
		}
		ss = append(ss, s)
	}

	firedIDs := []uuid.UUID{}
	for _, s := range ss {
		if _, err := e.pg.Exec(ctx,
			`UPDATE collectors SET status='stale', updated_at=NOW() WHERE id=$1`, s.id); err != nil {
			e.log.Warn("collector_status_update_failed", "id", s.id, "err", err)
			continue
		}
		dedupe := fmt.Sprintf("collector-down|%s", s.id)
		var existingID uuid.UUID
		err := e.pg.QueryRow(ctx,
			`SELECT id FROM alerts WHERE dedupe_key=$1 AND state='firing' LIMIT 1`,
			dedupe,
		).Scan(&existingID)
		if err == nil {
			_, _ = e.pg.Exec(ctx,
				`UPDATE alerts SET last_seen_at=$1, updated_at=NOW() WHERE id=$2`,
				now, existingID)
			continue
		}
		id := uuid.New()
		labels, _ := json.Marshal(map[string]string{"kind": "collector_down", "collector": s.name})
		summary := fmt.Sprintf("Collector %s has not reported since %s",
			s.name, s.lastSeen.Format("2006-01-02 15:04 MST"))
		_, err = e.pg.Exec(ctx, `
			INSERT INTO alerts (id, site_id, collector_id, severity, state, dedupe_key,
				summary, first_seen_at, last_seen_at, labels_json, created_at, updated_at)
			VALUES ($1,$2,$3,'major','firing',$4,$5,$6,$6,$7,NOW(),NOW())
		`, id, s.siteID, s.id, dedupe, summary, now, labels)
		if err != nil {
			e.log.Warn("collector_alert_insert_failed", "id", s.id, "err", err)
			continue
		}
		firedIDs = append(firedIDs, id)
	}
	for _, id := range firedIDs {
		e.enqueueNotify(ctx, "fire", id)
	}
	e.log.Info("collectors_swept", "stale", len(ss), "fired", len(firedIDs))
}

// enqueueNotify: drops a job onto the existing ARQ Redis queue so the
// Python worker's dispatch_fire / dispatch_resolve runs. ARQ's wire
// format is msgpack; we don't import msgpack here — instead we LPUSH a
// JSON shim onto a side channel that the Python worker reads via a
// thin adapter (`arq_bridge`). Keeps this service free of msgpack deps.
func (e *engine) enqueueNotify(ctx context.Context, kind string, alertID uuid.UUID) {
	payload := map[string]string{"kind": kind, "alert_id": alertID.String()}
	raw, _ := json.Marshal(payload)
	if err := e.rdb.LPush(ctx, "dcim:notify:bridge", raw).Err(); err != nil {
		e.log.Warn("notify_enqueue_failed", "err", err, "kind", kind, "alert", alertID)
	}
}

