package freshness

import (
	"context"
	"errors"
	"testing"
)

type fakeQ struct {
	calls   int
	flipped int64
	err     error
}

func (f *fakeQ) FlipStaleTelemetrySources(_ context.Context) (int64, error) {
	f.calls++
	if f.err != nil {
		return 0, f.err
	}
	return f.flipped, nil
}

func TestRun_ReportsFlippedCount(t *testing.T) {
	q := &fakeQ{flipped: 7}
	j := &Job{Q: q}
	out, err := j.Run(context.Background())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if q.calls != 1 {
		t.Errorf("expected exactly 1 call, got %d", q.calls)
	}
	if v, ok := out["flipped"].(int64); !ok || v != 7 {
		t.Errorf("output flipped: got %v, want 7", out["flipped"])
	}
}

func TestRun_NilQuerier_Rejected(t *testing.T) {
	j := &Job{}
	if _, err := j.Run(context.Background()); err == nil {
		t.Error("expected error for nil Q")
	}
}

func TestRun_DBError_Propagated(t *testing.T) {
	q := &fakeQ{err: errors.New("connection refused")}
	j := &Job{Q: q}
	if _, err := j.Run(context.Background()); err == nil {
		t.Error("expected error to propagate from Querier")
	}
}

func TestRun_ZeroFlipped_StillSucceeds(t *testing.T) {
	// A sweep cycle that finds no stale rows is the steady-state
	// happy path (everything's fresh). Make sure the harness doesn't
	// confuse "0 rows updated" with an error.
	q := &fakeQ{flipped: 0}
	j := &Job{Q: q}
	out, err := j.Run(context.Background())
	if err != nil {
		t.Fatalf("zero-flipped path should succeed; got err=%v", err)
	}
	if v, ok := out["flipped"].(int64); !ok || v != 0 {
		t.Errorf("output flipped: got %v, want 0", out["flipped"])
	}
}

func TestName_Matches(t *testing.T) {
	j := &Job{}
	if j.Name() != Name {
		t.Errorf("Name(): got %q, want %q", j.Name(), Name)
	}
	if Name != "freshness_sweep" {
		t.Errorf("package-level Name constant changed unexpectedly: %q", Name)
	}
}
