// Command otter-go-worker drains ARIN Reg-RWS jobs from the
// lir_allocations table. Designed to run as a separate k8s
// Deployment so its lifecycle is independent of the API process —
// the API stays responsive while the worker drains, and a worker
// crash doesn't take user traffic down.
//
// Configuration:
//
//	DCIM_POSTGRES_DSN_RAW    same DSN otter-go uses
//	WORKER_TICK_SECONDS      poll interval (default 30)
//	WORKER_MAX_PER_TICK      max jobs drained per tick (default 10)
//	WORKER_HEALTH_ADDR       /healthz bind (default :8080)
//
// ARIN endpoint / API key / enabled flag come from system_settings;
// operators rotate the key via the admin Settings UI without
// restarting this binary.
//
// Concurrency: safe to run with replicas>1. The claim query uses
// FOR UPDATE OF lir_allocations SKIP LOCKED so two replicas pick up
// disjoint rows. Crash recovery is automatic — a tx that aborts
// releases its locks, so the next tick claims the row again.
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
	"github.com/usg-dcim/packages/otter-go/internal/lir/arin"
	"github.com/usg-dcim/packages/shared-go/env"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	pgDSN := env.String("DCIM_POSTGRES_DSN_RAW", "postgres://dcim:dcim@postgres:5432/dcim")
	tick := time.Duration(env.Int("WORKER_TICK_SECONDS", 30)) * time.Second
	maxPerTick := env.Int("WORKER_MAX_PER_TICK", 10)
	healthAddr := env.String("WORKER_HEALTH_ADDR", ":8080")

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
	worker := arin.NewWorker(pool, q, log)
	worker.Tick = tick
	worker.MaxPerTick = maxPerTick

	// /healthz endpoint — same shape as otter-go so k8s liveness/
	// readiness probes can target either binary without per-pod
	// config divergence. Runs on its own port so the pod doesn't
	// have to multiplex with the worker loop.
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
		log.Info("worker_health_listen", "addr", healthAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("worker_health_failed", "err", err)
			cancel()
		}
	}()

	log.Info("arin_worker_starting",
		"tick", tick.String(), "max_per_tick", maxPerTick)
	worker.Run(ctx)

	log.Info("arin_worker_shutdown")
	shCtx, shCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shCancel()
	_ = srv.Shutdown(shCtx)
}
