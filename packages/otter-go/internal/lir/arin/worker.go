// Worker loop for the ARIN Reg-RWS submission direction. One
// goroutine per process running Run(); two workers can run side by
// side and the FOR UPDATE SKIP LOCKED claim guarantees disjoint
// rows. Crash recovery is automatic — the row lock releases when
// the tx aborts, so the next tick picks the row up again.
//
// Per-tick flow:
//   1. Load Config from system_settings. If !Enabled, sleep.
//   2. In a loop (capped at maxPerTick): begin tx, claim one job,
//      call ARIN, update status, commit. Stop when no row is
//      claimable.
//   3. Sleep until the next ticker fire.
//
// Tests bypass the tx machinery by calling ProcessOne directly with
// a fake JobQuerier. The Run loop only matters in production.
package arin

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// JobQuerier is the slice of sqlc methods ProcessOne needs. Wrapped
// in a tx by Run; tests substitute an in-memory fake that records
// each call.
type JobQuerier interface {
	ClaimNextArinSubmitJob(ctx context.Context, maxAttempts int32) (dbq.ArinSubmitJobRow, error)
	MarkArinRegistered(ctx context.Context, arg dbq.MarkArinRegisteredParams) error
	MarkArinFailed(ctx context.Context, arg dbq.MarkArinFailedParams) error
}

// RemoveJobQuerier is the slice ProcessOneRemove needs. Split from
// JobQuerier so test fakes can implement just the direction they
// care about without stubbing everything.
type RemoveJobQuerier interface {
	ClaimNextArinRemoveJob(ctx context.Context, maxAttempts int32) (dbq.ArinRemoveJobRow, error)
	MarkArinRemoved(ctx context.Context, id uuid.UUID) error
	MarkArinFailed(ctx context.Context, arg dbq.MarkArinFailedParams) error
}

// SubmitClient is what the worker calls per job. *Client satisfies
// it; tests substitute a fake that returns a canned SubmitResult or
// error so the path through MarkArinRegistered / MarkArinFailed can
// be exercised without touching the network.
type SubmitClient interface {
	SubmitReassignDetailed(ctx context.Context, job dbq.ArinSubmitJobRow) (SubmitResult, error)
}

// RemoveClient is the deassignment counterpart. *Client satisfies
// it; tests substitute a fake.
type RemoveClient interface {
	RemoveReassignment(ctx context.Context, parentNetHandle, netHandle string) error
}

// TxBeginner is the slim subset of *pgxpool.Pool the worker needs.
// Lets the production wiring use a real pool while tests pass a
// stub that just runs the closure inline.
type TxBeginner interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// arinClient bundles both directions a single *Client offers. Phase
// 5 used the SubmitClient alias for ProcessOne; phase 6 needs the
// remove side too. NewWorker hands a bundle implementing both.
type arinClient interface {
	SubmitClient
	RemoveClient
}

// Worker holds the long-lived configuration. Run() consumes ctx and
// returns when ctx is cancelled.
type Worker struct {
	Pool       TxBeginner
	Settings   SettingsQuerier
	NewClient  func(Config) arinClient
	Tick       time.Duration
	MaxPerTick int
	Log        *slog.Logger
}

// NewWorker returns a Worker with sensible defaults. The caller
// supplies the pool + a SettingsQuerier (*dbq.Queries built off the
// same pool works); NewClient defaults to a stdlib HTTP client.
func NewWorker(pool TxBeginner, settings SettingsQuerier, log *slog.Logger) *Worker {
	if log == nil {
		log = slog.Default()
	}
	return &Worker{
		Pool:     pool,
		Settings: settings,
		NewClient: func(cfg Config) arinClient {
			return NewClient(cfg, nil)
		},
		Tick:       30 * time.Second,
		MaxPerTick: 10,
		Log:        log,
	}
}

// Run drives the worker loop until ctx is cancelled. Returns when
// the ticker channel closes (which only happens on ctx cancel).
func (w *Worker) Run(ctx context.Context) {
	t := time.NewTicker(w.Tick)
	defer t.Stop()
	// Fire one tick immediately so a newly-started worker doesn't
	// idle for the whole interval before draining the queue.
	w.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.tick(ctx)
		}
	}
}

func (w *Worker) tick(ctx context.Context) {
	cfg := LoadConfig(ctx, w.Settings)
	if !cfg.Enabled {
		w.Log.Debug("arin_worker_disabled_skipping_tick")
		return
	}
	client := w.NewClient(cfg)
	// Drain the submit queue first, then the remove queue. Capping
	// the combined draw at MaxPerTick keeps the tick bounded even
	// when both queues are full.
	budget := w.MaxPerTick
	for i := 0; i < budget; i++ {
		more, err := w.processOneTxWrapped(ctx, client)
		if err != nil {
			w.Log.Error("arin_worker_tick_error", "direction", "submit", "err", err)
			return
		}
		if !more {
			break
		}
		budget--
	}
	for i := 0; i < budget; i++ {
		more, err := w.processOneRemoveTxWrapped(ctx, client)
		if err != nil {
			w.Log.Error("arin_worker_tick_error", "direction", "remove", "err", err)
			return
		}
		if !more {
			return
		}
	}
}

// processOneTxWrapped opens a tx, builds a Querier scoped to it, and
// delegates to ProcessOne. Returns (more, err) — more=true when a
// job was processed (caller may immediately try another); false
// when the queue is empty for now.
func (w *Worker) processOneTxWrapped(ctx context.Context, client SubmitClient) (bool, error) {
	tx, err := w.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	// Rollback is a no-op after a successful Commit, so deferring it
	// is safe even on the success path.
	defer func() { _ = tx.Rollback(ctx) }()

	q := dbq.New(tx)
	more, err := w.ProcessOne(ctx, q, client)
	if err != nil {
		return more, err
	}
	return more, tx.Commit(ctx)
}

// processOneRemoveTxWrapped is the remove-direction sibling of
// processOneTxWrapped. Same tx semantics: claim with FOR UPDATE
// SKIP LOCKED, call ARIN, update status, commit.
func (w *Worker) processOneRemoveTxWrapped(ctx context.Context, client RemoveClient) (bool, error) {
	tx, err := w.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := dbq.New(tx)
	more, err := w.ProcessOneRemove(ctx, q, client)
	if err != nil {
		return more, err
	}
	return more, tx.Commit(ctx)
}

// ProcessOne does the actual job work: claim one row, call ARIN,
// update status. Exposed so unit tests can drive it directly with
// a fake JobQuerier + SubmitClient.
//
// Returns (false, nil) when no row is claimable — caller should
// stop the loop. Returns (true, nil) on a successful processing
// cycle (whether the ARIN call itself succeeded or failed —
// "processed" means we updated the row's arin_status).
func (w *Worker) ProcessOne(ctx context.Context, q JobQuerier, client SubmitClient) (bool, error) {
	job, err := q.ClaimNextArinSubmitJob(ctx, MaxAttempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	result, callErr := client.SubmitReassignDetailed(ctx, job)
	if callErr != nil {
		w.Log.Warn("arin_submit_failed",
			"allocation_id", job.AllocationID.String(),
			"attempt", job.ArinAttempts+1,
			"transient", errors.Is(callErr, ErrTransient),
			"err", callErr.Error())
		if mErr := q.MarkArinFailed(ctx, dbq.MarkArinFailedParams{
			ID: job.AllocationID, Error: callErr.Error(),
		}); mErr != nil {
			return true, mErr
		}
		return true, nil
	}
	w.Log.Info("arin_submit_registered",
		"allocation_id", job.AllocationID.String(),
		"net_handle", result.NetHandle)
	if mErr := q.MarkArinRegistered(ctx, dbq.MarkArinRegisteredParams{
		ID: job.AllocationID, NetHandle: result.NetHandle,
	}); mErr != nil {
		return true, mErr
	}
	return true, nil
}

// ProcessOneRemove is the deassignment-direction sibling of
// ProcessOne. Same shape; calls RemoveReassignment and dispatches
// to MarkArinRemoved (success) or MarkArinFailed (any error). The
// underlying *Client classifies transient vs permanent — the
// worker just records the error string; the SQL backoff CASE keeps
// the cadence aligned with the submit direction.
func (w *Worker) ProcessOneRemove(ctx context.Context, q RemoveJobQuerier, client RemoveClient) (bool, error) {
	job, err := q.ClaimNextArinRemoveJob(ctx, MaxAttempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	callErr := client.RemoveReassignment(ctx, job.ParentNetHandle, job.NetHandle)
	if callErr != nil {
		w.Log.Warn("arin_remove_failed",
			"allocation_id", job.AllocationID.String(),
			"attempt", job.ArinAttempts+1,
			"transient", errors.Is(callErr, ErrTransient),
			"err", callErr.Error())
		if mErr := q.MarkArinFailed(ctx, dbq.MarkArinFailedParams{
			ID: job.AllocationID, Error: callErr.Error(),
		}); mErr != nil {
			return true, mErr
		}
		return true, nil
	}
	w.Log.Info("arin_remove_succeeded",
		"allocation_id", job.AllocationID.String(),
		"net_handle", job.NetHandle)
	if mErr := q.MarkArinRemoved(ctx, job.AllocationID); mErr != nil {
		return true, mErr
	}
	return true, nil
}

// Compile-time assurance: a single client value can satisfy both
// directions, which is what NewWorker hands to processOneTxWrapped
// and processOneRemoveTxWrapped per tick.
var _ arinClient = (*Client)(nil)
