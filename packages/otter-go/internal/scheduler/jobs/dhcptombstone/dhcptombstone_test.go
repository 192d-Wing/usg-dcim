package dhcptombstone

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeQ struct {
	gotCutoff time.Time
	purged    int64
	err       error
}

func (f *fakeQ) PurgeExpiredDhcpScopeTombstones(_ context.Context, cutoff time.Time) (int64, error) {
	f.gotCutoff = cutoff
	if f.err != nil {
		return 0, f.err
	}
	return f.purged, nil
}

func TestRun_PassesCorrectCutoffAndReportsPurged(t *testing.T) {
	frozen := time.Date(2026, 5, 31, 3, 30, 0, 0, time.UTC)
	q := &fakeQ{purged: 7}
	j := &Job{Q: q, RetentionDays: 30, Now: func() time.Time { return frozen }}

	out, err := j.Run(context.Background())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	wantCutoff := frozen.Add(-30 * 24 * time.Hour)
	if !q.gotCutoff.Equal(wantCutoff) {
		t.Errorf("cutoff: got %v, want %v", q.gotCutoff, wantCutoff)
	}
	if v, ok := out["purged"].(int64); !ok || v != 7 {
		t.Errorf("purged: got %v (ok=%v), want 7", out["purged"], ok)
	}
	if v, ok := out["retention_days"].(int); !ok || v != 30 {
		t.Errorf("retention_days: got %v (ok=%v), want 30", out["retention_days"], ok)
	}
	if _, ok := out["cutoff"].(string); !ok {
		t.Errorf("cutoff: missing or wrong type; got %v", out["cutoff"])
	}
}

func TestRun_NilQuerier_Rejected(t *testing.T) {
	j := &Job{RetentionDays: 30}
	if _, err := j.Run(context.Background()); err == nil {
		t.Error("expected error for nil Q")
	}
}

func TestRun_NonPositiveRetention_Rejected(t *testing.T) {
	q := &fakeQ{}
	// Guard against misconfig: a 0-day retention would purge every
	// tombstone the moment it landed (well, the moment NOW() ticked
	// past deleted_at). The job should refuse to run rather than
	// nuke a freshly-soft-deleted scope before the operator can
	// reach for the undo button.
	for _, days := range []int{0, -1} {
		j := &Job{Q: q, RetentionDays: days}
		if _, err := j.Run(context.Background()); err == nil {
			t.Errorf("RetentionDays=%d: expected error, got nil", days)
		}
	}
}

func TestRun_DBError_Propagated(t *testing.T) {
	q := &fakeQ{err: errors.New("conn refused")}
	j := &Job{Q: q, RetentionDays: 30}
	if _, err := j.Run(context.Background()); err == nil {
		t.Error("expected error to propagate from Querier")
	}
}

func TestRun_ZeroPurged_StillSucceeds(t *testing.T) {
	q := &fakeQ{purged: 0}
	j := &Job{Q: q, RetentionDays: 30}
	out, err := j.Run(context.Background())
	if err != nil {
		t.Fatalf("zero-purged path should succeed; got err=%v", err)
	}
	if v, ok := out["purged"].(int64); !ok || v != 0 {
		t.Errorf("purged: got %v (ok=%v), want 0", out["purged"], ok)
	}
}

func TestName_Matches(t *testing.T) {
	j := &Job{}
	if j.Name() != Name {
		t.Errorf("Name(): got %q, want %q", j.Name(), Name)
	}
	if Name != "dhcp_scope_tombstone_purge" {
		t.Errorf("package-level Name constant changed unexpectedly: %q", Name)
	}
}
