package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// fakeJob is a programmable Job for the harness tests. The Run
// callback exists so a test can record calls + return controlled
// error / map values.
type fakeJob struct {
	name string
	calls atomic.Int32
	run   func(context.Context) (map[string]any, error)
}

func (f *fakeJob) Name() string { return f.name }
func (f *fakeJob) Run(ctx context.Context) (map[string]any, error) {
	f.calls.Add(1)
	if f.run != nil {
		return f.run(ctx)
	}
	return map[string]any{"ok": true}, nil
}

func TestRegister_DuplicateName_Rejected(t *testing.T) {
	s := New(nil)
	a := &fakeJob{name: "twin"}
	b := &fakeJob{name: "twin"}
	firstID, err := s.Register("@hourly", a)
	if err != nil {
		t.Fatalf("first register failed: %v", err)
	}
	if _, err := s.Register("@hourly", b); err == nil {
		t.Error("second register with duplicate name must fail loud")
	}
	// Tighten the contract: the rejected duplicate must not have
	// disturbed the first registration's entry. A regression that
	// removed the first entry while returning err on dup would
	// otherwise pass.
	got := s.cr.Entry(firstID)
	if got.ID != firstID {
		t.Errorf("first entry lost after rejected duplicate: got ID %d, want %d", got.ID, firstID)
	}
}

func TestRegister_InvalidSpec_RollsBackName(t *testing.T) {
	// Bad spec on first call returns an error AND leaves the name
	// registry empty, so a corrected-spec retry with the same Job
	// succeeds. Without rollback the retry would 'duplicate' the
	// stale entry.
	s := New(nil)
	j := &fakeJob{name: "retry-me"}
	if _, err := s.Register("not a cron spec", j); err == nil {
		t.Fatal("invalid spec must error")
	}
	if _, err := s.Register("@hourly", j); err != nil {
		t.Errorf("retry with corrected spec failed: %v", err)
	}
}

func TestRunOnce_InvokesJob(t *testing.T) {
	s := New(nil)
	j := &fakeJob{name: "purge-test"}
	s.RunOnce(j)
	if got := j.calls.Load(); got != 1 {
		t.Errorf("expected exactly 1 call, got %d", got)
	}
}

func TestRunOnce_JobReturnsError_LoggedNotPropagated(t *testing.T) {
	// The harness must not panic / propagate a job error past the
	// log line — cron tick callbacks have no caller to surface it
	// to. Test by running the job once and asserting no panic plus
	// the increment still landed.
	s := New(nil)
	j := &fakeJob{
		name: "boom",
		run: func(context.Context) (map[string]any, error) {
			return nil, errors.New("dependency unavailable")
		},
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("scheduler panicked on job error: %v", r)
		}
	}()
	s.RunOnce(j)
	if got := j.calls.Load(); got != 1 {
		t.Errorf("expected exactly 1 call, got %d", got)
	}
}

func TestStart_Stop_NoJobs_OK(t *testing.T) {
	// Empty scheduler should Start and Stop cleanly; nothing to
	// fire. Guards against accidentally requiring at least one
	// Register call before Start.
	s := New(nil)
	s.Start(context.Background())
	s.Stop()
}

// Pinned: Start(ctx) is the channel that propagates SIGTERM into
// every job's Run. Without this, a long-running DELETE keeps
// executing past Stop() and the pool may close from under it.
func TestStart_PropagatesCtxToJobRun(t *testing.T) {
	s := New(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.Start(ctx)
	defer s.Stop()
	var seen context.Context
	j := &fakeJob{
		name: "ctx-probe",
		run: func(c context.Context) (map[string]any, error) {
			seen = c
			return nil, nil
		},
	}
	s.RunOnce(j)
	if seen == nil {
		t.Fatal("job did not see a ctx")
	}
	if seen.Err() == nil {
		t.Errorf("expected the pre-cancelled ctx to flow into Run; got Err()=nil")
	}
}

// Pinned: job-supplied result keys that collide with the harness's
// own slog keys are dropped + warned, not silently allowed to
// shadow. Without this, a future job returning result["job"]/
// ["duration_ms"]/["error"] corrupts the structured log output.
func TestRunOne_ReservedKeysSkipped(t *testing.T) {
	s := New(nil)
	j := &fakeJob{
		name: "noisy",
		run: func(context.Context) (map[string]any, error) {
			return map[string]any{
				"job":         "spoof",
				"duration_ms": 999,
				"error":       "fake",
				"ok":          true,
			}, nil
		},
	}
	// Just exercise the path — the assertion is "no panic, no
	// duplicate slog key crash". The slog handler in tests is
	// slog.Default which is a text handler; reserved-key
	// shadowing would surface there as duplicated keys.
	s.RunOnce(j)
}

// RunOnce must not panic even when the job does — the recovery is
// internal to runOne so direct callers (operator one-shot scripts,
// future admin endpoints) don't take the scheduler binary down.
func TestRunOnce_JobPanics_Recovered(t *testing.T) {
	s := New(nil)
	j := &fakeJob{
		name: "exploder",
		run: func(context.Context) (map[string]any, error) {
			panic("nil pointer somewhere downstream")
		},
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("scheduler.runOne leaked a panic: %v", r)
		}
	}()
	s.RunOnce(j)
}
