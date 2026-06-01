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
//	                                dns_purge_metrics job (default 14,
//	                                matching Python's
//	                                settings.dns_metrics_retention_days)
//	DCIM_DNS_PURGE_CRON            cron spec for dns_purge_metrics
//	                                (default "23 * * * *" — hourly at :23)
//	DCIM_FRESHNESS_SWEEP_CRON      cron spec for freshness_sweep
//	                                (default "*/5 * * * *" — every 5 min)
//	DCIM_DNS_SYNC_FROM_IPAM_CRON   cron spec for dns_sync_from_ipam
//	                                (default "4-59/5 * * * *" — every 5 min
//	                                offset 4 to spread vs freshness_sweep)
//	DCIM_DHCP_TOMBSTONE_PURGE_CRON cron spec for dhcp_scope_tombstone_purge
//	                                (default "30 3 * * *" — daily at 03:30)
//	DCIM_DHCP_TOMBSTONE_RETENTION_DAYS retention window for the
//	                                dhcp_scope_tombstone_purge job
//	                                (default 30, matching Python's
//	                                settings.dhcp_tombstone_retention_days)
//	DCIM_DNS_ROTATE_ZSKS_CRON      cron spec for dns_rotate_zsks
//	                                (default "17 3 * * *" — daily at 03:17)
//	DCIM_DHCP_BUNDLE_RERENDER_CRON cron spec for dhcp_bundle_rerender
//	                                (default "*/2 * * * *" — every 2 min,
//	                                polling backstop for Python's
//	                                event-driven rerender_dhcp_bundle)
//	SCHEDULER_HEALTH_ADDR          /healthz bind (default :8080)
//
// Cron defaults mirror Python's arq schedules in worker.py. Override
// via the env vars for ops throttling (e.g. during a backfill) instead
// of rebuilding the binary.
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
	"github.com/usg-dcim/packages/otter-go/internal/scheduler/jobs/dhcpbundle"
	"github.com/usg-dcim/packages/otter-go/internal/scheduler/jobs/dhcptombstone"
	"github.com/usg-dcim/packages/otter-go/internal/scheduler/jobs/dnspurge"
	"github.com/usg-dcim/packages/otter-go/internal/scheduler/jobs/dnssecrotate"
	"github.com/usg-dcim/packages/otter-go/internal/scheduler/jobs/dnssync"
	"github.com/usg-dcim/packages/otter-go/internal/scheduler/jobs/freshness"
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
	// Default spec "*/5 * * * *" mirrors Python's
	// cron(freshness_sweep, minute=set(range(0, 60, 5))) — every 5
	// minutes. Flips current→stale for telemetry sources whose last
	// successful poll is older than max(60s, 3× poll interval).
	freshnessCron := env.String("DCIM_FRESHNESS_SWEEP_CRON", "*/5 * * * *")
	// Default spec "4-59/5 * * * *" mirrors Python's
	// cron(dns_sync_from_ipam, minute=set(range(4, 60, 5))) — every 5
	// minutes at :04, :09, :14, … The :04 offset is intentional: it
	// staggers vs freshness_sweep (:00, :05, …) so two heavy passes
	// don't collide on a single tick.
	dnsSyncCron := env.String("DCIM_DNS_SYNC_FROM_IPAM_CRON", "4-59/5 * * * *")
	// Default spec "30 3 * * *" mirrors Python's
	// cron(dhcp_scope_tombstone_purge, hour={3}, minute={30}) — daily
	// at 03:30 UTC. Off-hours so the DELETE doesn't compete with
	// operator-initiated traffic. Default 30-day retention mirrors
	// Python's settings.dhcp_tombstone_retention_days.
	dhcpTombstoneCron := env.String("DCIM_DHCP_TOMBSTONE_PURGE_CRON", "30 3 * * *")
	dhcpTombstoneRetention := env.Int("DCIM_DHCP_TOMBSTONE_RETENTION_DAYS", 30)
	// Default spec "17 3 * * *" mirrors Python's
	// cron(dns_rotate_zsks, hour={3}, minute={17}). Off-hours,
	// off-round minute so it doesn't collide with dhcp_tombstone_purge
	// (:30) or any future top-of-hour ticks. Daily cadence is fine —
	// the per-zone rotation threshold is multi-day (zsk_rotation_days
	// typically 30+), so checking every 24h has 24× headroom over the
	// finest-grained rotation policy.
	zskRotateCron := env.String("DCIM_DNS_ROTATE_ZSKS_CRON", "17 3 * * *")
	// Default spec "*/2 * * * *" runs the dhcp_bundle_rerender
	// polling backstop every 2 minutes. Python's
	// rerender_dhcp_bundle is event-driven (enqueued per-server on
	// every scope mutation); the Go scheduler is cron-only, so the
	// polling cadence sets the worst-case staleness window for the
	// cache. The HTTP bundle endpoint's live-render fallback makes
	// any cache-miss request correct, so 2 minutes is a balance
	// between log volume and how quickly an operator can see their
	// scope mutation propagated to the puller's etag header.
	dhcpBundleCron := env.String("DCIM_DHCP_BUNDLE_RERENDER_CRON", "*/2 * * * *")
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

	// mustRegister wraps sch.Register: every job in this binary
	// follows the same "log + exit on register failure" pattern,
	// so collapsing it into one helper kept the per-job lines as
	// one-liners and cut sonar's flagged duplication across the
	// six call sites below.
	mustRegister := func(spec string, job scheduler.Job) {
		if _, err := sch.Register(spec, job); err != nil {
			log.Error("scheduler_register_failed", "job", job.Name(), "err", err)
			os.Exit(1)
		}
	}

	// Hourly DNS-metrics retention at :23 — matches Python's
	// cron(dns_purge_metrics, minute={23}) in worker.py:557. The :23
	// slot is intentionally off-round so two scheduler replicas don't
	// fire simultaneously with other top-of-hour jobs.
	mustRegister(purgeCron, &dnspurge.Job{Q: q, RetentionDays: retentionDays})
	mustRegister(freshnessCron, &freshness.Job{Q: q})
	mustRegister(dnsSyncCron, &dnssync.Job{Q: q})
	mustRegister(dhcpTombstoneCron, &dhcptombstone.Job{Q: q, RetentionDays: dhcpTombstoneRetention})
	mustRegister(zskRotateCron, &dnssecrotate.Job{Q: q, Log: log})
	mustRegister(dhcpBundleCron, &dhcpbundle.Job{Q: q, Log: log})

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
		"dns_metrics_retention_days", retentionDays,
		"dhcp_tombstone_retention_days", dhcpTombstoneRetention,
		"addr", healthAddr)

	<-ctx.Done()
	log.Info("otter_go_scheduler_shutdown")
	sch.Stop()
	shCtx, shCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shCancel()
	_ = srv.Shutdown(shCtx)
}
