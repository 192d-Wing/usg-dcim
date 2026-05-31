package dnspurge

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeQ struct {
	lastCutoff time.Time
	deleted    int64
	err        error
}

func (f *fakeQ) DeleteDnsServerMetricsSamplesOlderThan(_ context.Context, cutoff time.Time) (int64, error) {
	f.lastCutoff = cutoff
	if f.err != nil {
		return 0, f.err
	}
	return f.deleted, nil
}

func TestRun_PassesCutoffAndReportsDeleted(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	q := &fakeQ{deleted: 42}
	j := &Job{Q: q, RetentionDays: 30, Now: func() time.Time { return now }}
	out, err := j.Run(context.Background())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	want := now.Add(-30 * 24 * time.Hour)
	if !q.lastCutoff.Equal(want) {
		t.Errorf("cutoff: got %s, want %s", q.lastCutoff, want)
	}
	if v, ok := out["deleted"].(int64); !ok || v != 42 {
		t.Errorf("output deleted: got %v, want 42", out["deleted"])
	}
	if v, ok := out["retention_days"].(int); !ok || v != 30 {
		t.Errorf("output retention_days: got %v, want 30", out["retention_days"])
	}
}

func TestRun_NilQuerier_Rejected(t *testing.T) {
	j := &Job{RetentionDays: 30}
	_, err := j.Run(context.Background())
	if err == nil {
		t.Error("expected error for nil Q")
	}
}

func TestRun_ZeroRetention_Rejected(t *testing.T) {
	// 0 / negative would mean "delete everything including future
	// rows", which is a misconfiguration we want to fail loud on.
	j := &Job{Q: &fakeQ{}, RetentionDays: 0}
	if _, err := j.Run(context.Background()); err == nil {
		t.Error("expected error for RetentionDays=0")
	}
	j.RetentionDays = -1
	if _, err := j.Run(context.Background()); err == nil {
		t.Error("expected error for negative RetentionDays")
	}
}

func TestRun_DBError_Propagated(t *testing.T) {
	q := &fakeQ{err: errors.New("connection refused")}
	j := &Job{Q: q, RetentionDays: 30}
	_, err := j.Run(context.Background())
	if err == nil {
		t.Error("expected error to propagate from Querier")
	}
}

func TestName_Matches(t *testing.T) {
	j := &Job{}
	if j.Name() != Name {
		t.Errorf("Name(): got %q, want %q", j.Name(), Name)
	}
	if Name != "dns_purge_metrics" {
		t.Errorf("package-level Name constant changed unexpectedly: %q", Name)
	}
}
