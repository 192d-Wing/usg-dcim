// Package scheduler is the Go cron harness for otter-go-scheduler.
// It runs the periodic jobs Python's arq worker (packages/otter/
// src/dcim/worker.py) historically owned — alerts evaluation,
// collectors sweep, DNS retention/rotate/sync, DHCP push/drift/
// tombstone, IPAM utilization, notification bridge, etc. The first
// landed job is dns_purge_metrics; the rest port one at a time.
//
// Library choice: robfig/cron/v3. It's the de-facto standard Go
// scheduler — std-cron grammar, std-context support, std-lib clock
// (overridable for tests via cron.WithLocation + WithChain), small
// surface. gocron was the alternative; robfig/cron's smaller API
// felt easier to wrap and review.
//
// Job interface: implementations receive a single ctx and return an
// error + a logged map. The harness wraps each call with structured
// slog (start, duration, error, return map), so individual jobs
// stay focused on their domain work.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// Job is what callers register. Run is invoked on every cron tick
// for the schedule the caller passes to Register. Name is the
// stable, machine-readable identifier the harness logs.
type Job interface {
	Name() string
	Run(ctx context.Context) (map[string]any, error)
}

// Scheduler orchestrates a robfig/cron Cron and tracks the jobs
// registered against it. Use New() to construct, Register() to add
// jobs, Start() to begin running, Stop() to drain.
type Scheduler struct {
	log *slog.Logger
	cr  *cron.Cron
	mu  sync.Mutex
	// names is the running registry so Register fails loud on
	// duplicate identifiers instead of silently double-scheduling.
	names map[string]struct{}
	// parentCtx is the shutdown signal Start() captures. Every job
	// tick derives its per-run ctx from this — so Stop() (which
	// cancels parentCtx upstream via Start's caller) propagates
	// cancellation into long-running DELETE/SQL paths instead of
	// orphaning them past pool.Close(). Background by default so
	// tests using RunOnce don't need a ctx.
	parentCtx context.Context
}

// New constructs a Scheduler bound to its parent context. Pass a
// nil logger for slog.Default().
//
// The cron parses the standard 5-field spec (minute, hour, day-of-
// month, month, day-of-week) plus "@every Xs" / "@daily" / "@hourly"
// shorthands robfig/cron supports natively. Add seconds with
// cron.WithSeconds() if a future job needs sub-minute granularity —
// keeping the default 5-field grammar matches what Python's arq
// worker.py uses today.
//
// Clock injection: robfig/cron does NOT expose a clock override at
// the Cron level (WithLocation is for spec parsing, not the wall
// clock). Tests inject clocks at the Job level — see
// internal/scheduler/jobs/dnspurge.Job.Now.
func New(log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	// Wrap robfig's panic-recover with a slog-shaped Printer so panic
	// traces stay structured (production handler is JSON). cron's
	// DefaultLogger writes plain "cron: …" text to os.Stdout which
	// breaks JSON-aggregator pipelines.
	cronLog := cron.PrintfLogger(slog.NewLogLogger(log.Handler(), slog.LevelError))
	return &Scheduler{
		log:       log,
		cr:        cron.New(cron.WithChain(cron.Recover(cronLog))),
		names:     map[string]struct{}{},
		parentCtx: context.Background(),
	}
}

// Register schedules a Job on the given cron spec. Returns the
// cron entry ID so callers (or tests) can target it. Duplicate
// Job.Name() panics at registration time — silent double-registration
// would race the job's own concurrency assumptions.
func (s *Scheduler) Register(spec string, j Job) (cron.EntryID, error) {
	s.mu.Lock()
	if _, dup := s.names[j.Name()]; dup {
		s.mu.Unlock()
		return 0, fmt.Errorf("scheduler: duplicate job %q", j.Name())
	}
	s.names[j.Name()] = struct{}{}
	s.mu.Unlock()
	id, err := s.cr.AddFunc(spec, func() { s.runOne(j) })
	if err != nil {
		// Roll back the names entry so a syntax-failed Register
		// doesn't block a corrected retry with the same Job.
		s.mu.Lock()
		delete(s.names, j.Name())
		s.mu.Unlock()
		return 0, fmt.Errorf("scheduler: invalid spec %q for %q: %w", spec, j.Name(), err)
	}
	s.log.Info("scheduler_register", "job", j.Name(), "spec", spec)
	return id, nil
}

// runOne is the wrapper every tick (and RunOnce) goes through.
// Derives a per-run ctx from s.parentCtx so Stop()-triggered
// cancellation reaches long-running jobs. Times the call and
// structure-logs the outcome. The defer-recover makes RunOnce
// panic-safe — cron.Recover only wraps Cron-scheduled ticks, not
// direct RunOnce calls, so a panicking operator one-shot would
// otherwise kill the scheduler binary.
func (s *Scheduler) runOne(j Job) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("scheduler_job_panic", "job", j.Name(), "panic", fmt.Sprint(r))
		}
	}()
	start := time.Now()
	result, err := j.Run(s.parentCtx)
	dur := time.Since(start)
	args := []any{"job", j.Name(), "duration_ms", dur.Milliseconds()}
	for k, v := range result {
		// Skip keys that would shadow the harness's structured-log
		// keys ("job", "duration_ms", "error"). Logged loudly on
		// collision so future jobs surface the bug without rewriting
		// slog output.
		if k == "job" || k == "duration_ms" || k == "error" {
			s.log.Warn("scheduler_job_result_key_reserved", "job", j.Name(), "key", k)
			continue
		}
		args = append(args, k, v)
	}
	if err != nil {
		args = append(args, "error", err.Error())
		s.log.Error("scheduler_job_failed", args...)
		return
	}
	s.log.Info("scheduler_job_ok", args...)
}

// Start begins the cron loop with `ctx` as the shutdown signal
// every job's Run will see. Cancel ctx (or call Stop()) to drain.
// Non-blocking. Safe to call multiple times — robfig/cron ignores
// re-Starts; the second call is a no-op.
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	s.parentCtx = ctx
	s.mu.Unlock()
	s.cr.Start()
	s.log.Info("scheduler_started", "jobs", len(s.names))
}

// Stop drains the cron's internal queue and waits for any
// in-flight tick to complete. After Stop returns, no further job
// will fire.
func (s *Scheduler) Stop() {
	ctx := s.cr.Stop()
	<-ctx.Done()
	s.log.Info("scheduler_stopped")
}

// RunOnce invokes the job out-of-band — bypassing the cron schedule.
// Useful for one-shot operator commands ("run dns_purge_metrics
// right now") and as the test entry point.
func (s *Scheduler) RunOnce(j Job) {
	s.runOne(j)
}
