// Command otter-go-scheduler runs the Go port of Python's arq cron
// worker (packages/otter/src/dcim/worker.py). Designed to run as its
// own k8s Deployment so the cron tree stays independent of the API
// process. First landed job is dns_purge_metrics; the rest of the
// Python arq cron entries port one at a time.
//
// Configuration (env):
//
//	DCIM_POSTGRES_DSN_RAW          same DSN otter-go uses
//	DCIM_DNS_METRICS_RETENTION_DAYS retention window for the
//	                                dns_purge_metrics job (default 30,
//	                                matching Python's
//	                                settings.dns_metrics_retention_days)
//	SCHEDULER_HEALTH_ADDR          /healthz bind (default :8080)
//
// Cron spec for dns_purge_metrics defaults to "23 * * * *" — hourly
// at :23, matching Python's cron(dns_purge_metrics, minute={23}) in
// worker.py:557. Override via DCIM_DNS_PURGE_CRON for ops throttling
// (e.g. during a backfill).
//
// Concurrency: jobs autocommit per scheduler.Job.Run; if a future
// job needs tx semantics it pulls the pool from this main via the
// same TxBeginner pattern PR #206 introduced for bgp.rotate-batch.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
	"github.com/usg-dcim/packages/otter-go/internal/scheduler"
	"github.com/usg-dcim/packages/otter-go/internal/scheduler/jobs/dnspurge"
	"github.com/usg-dcim/packages/shared-go/env"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	pgDSN := env.String("DCIM_POSTGRES_DSN_RAW", "postgres://dcim:dcim@postgres:5432/dcim")
	// Default 14 days mirrors Python's settings.dns_metrics_retention_days.
	retentionDays := env.Int("DCIM_DNS_METRICS_RETENTION_DAYS", 14)
	// Default spec "23 * * * *" mirrors Python's
	// cron(dns_purge_metrics, minute={23}) — hourly at :23. Operators
	// can override (e.g. to throttle to daily during a backfill) via
	// the env var instead of rebuilding the binary.
	purgeCron := env.String("DCIM_DNS_PURGE_CRON", "23 * * * *")
	healthAddr := env.String("SCHEDULER_HEALTH_ADDR", ":8080")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	pgCtx, pgCancel := context.WithTimeout(ctx, 30*time.Second)
	pool, err := pgxpool.New(pgCtx, pgDSN)
	pgCancel()
	if err != nil {
		log.Error("pg_connect_failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	q := dbq.New(pool)
	sch := scheduler.New(log)

	// Hourly DNS-metrics retention at :23 — matches Python's
	// cron(dns_purge_metrics, minute={23}) in worker.py:557. The :23
	// slot is intentionally off-round so two scheduler replicas don't
	// fire simultaneously with other top-of-hour jobs.
	if _, err := sch.Register(purgeCron, &dnspurge.Job{
		Q:             q,
		RetentionDays: retentionDays,
	}); err != nil {
		log.Error("scheduler_register_failed", "err", err)
		os.Exit(1)
	}

	// /healthz + /readyz mirror the otter-go-worker shape so k8s
	// probes can target either binary without per-pod config
	// divergence.
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		pingCtx, c := context.WithTimeout(r.Context(), 2*time.Second)
		defer c()
		if err := pool.Ping(pingCtx); err != nil {
			httpx.Error(w, http.StatusServiceUnavailable, "db unavailable")
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	srv := &http.Server{
		Addr:              healthAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Info("scheduler_health_listen", "addr", healthAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("scheduler_health_failed", "err", err)
			cancel()
		}
	}()

	// Pass ctx so a SIGTERM derived cancellation propagates into
	// every in-flight job's Run — matters once the longer DHCP /
	// alerts evaluation jobs port and exceed terminationGracePeriod
	// if the harness's ctx doesn't reach them.
	sch.Start(ctx)
	log.Info("otter_go_scheduler_started",
		"retention_days", retentionDays, "addr", healthAddr)

	<-ctx.Done()
	log.Info("otter_go_scheduler_shutdown")
	sch.Stop()
	shCtx, shCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shCancel()
	_ = srv.Shutdown(shCtx)
}
