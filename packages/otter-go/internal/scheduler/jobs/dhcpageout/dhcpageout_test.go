// Tests for the dhcp_age_out scheduler job. Pure-SQL job — the two
// query calls are exercised against a fake Querier that records the
// cutoff each gets. Per-pass row counts thread through to the
// result map.
package dhcpageout

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeQ struct {
	deprecateCutoff time.Time
	deprecateRows   int64
	deprecateErr    error

	deleteCutoff time.Time
	deleteRows   int64
	deleteErr    error
}

func (f *fakeQ) DeprecateExpiredDhcpLeases(_ context.Context, cutoff time.Time) (int64, error) {
	f.deprecateCutoff = cutoff
	return f.deprecateRows, f.deprecateErr
}
func (f *fakeQ) DeleteDeprecatedDhcpLeases(_ context.Context, cutoff time.Time) (int64, error) {
	f.deleteCutoff = cutoff
	return f.deleteRows, f.deleteErr
}

func TestRun_NilQuerier_Rejected(t *testing.T) {
	if _, err := (&Job{}).Run(context.Background()); err == nil {
		t.Error("expected err for nil Q")
	}
}

func TestRun_HappyPath_ThreadsCutoffsAndCounts(t *testing.T) {
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	f := &fakeQ{deprecateRows: 3, deleteRows: 7}
	j := &Job{Q: f, Now: func() time.Time { return now }}
	out, err := j.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out["deprecated"].(int64) != 3 || out["deleted"].(int64) != 7 {
		t.Errorf("out = %+v, want deprecated=3 deleted=7", out)
	}
	// Default grace = 3600s; cutoff_deprecate = now - 1h.
	wantDeprecate := now.Add(-time.Hour)
	if !f.deprecateCutoff.Equal(wantDeprecate) {
		t.Errorf("deprecate cutoff = %v, want %v", f.deprecateCutoff, wantDeprecate)
	}
	// Default delete-after = 1 day; cutoff_delete = now - 24h.
	wantDelete := now.Add(-24 * time.Hour)
	if !f.deleteCutoff.Equal(wantDelete) {
		t.Errorf("delete cutoff = %v, want %v", f.deleteCutoff, wantDelete)
	}
}

func TestRun_CustomGraceAndDeleteWindow(t *testing.T) {
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	f := &fakeQ{}
	j := &Job{Q: f, GraceSeconds: 30, DeleteAfterDays: 7, Now: func() time.Time { return now }}
	_, err := j.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !f.deprecateCutoff.Equal(now.Add(-30 * time.Second)) {
		t.Errorf("deprecate cutoff = %v, want now-30s", f.deprecateCutoff)
	}
	if !f.deleteCutoff.Equal(now.Add(-7 * 24 * time.Hour)) {
		t.Errorf("delete cutoff = %v, want now-7d", f.deleteCutoff)
	}
}

func TestRun_DeprecateErr_Wrapped(t *testing.T) {
	f := &fakeQ{deprecateErr: errors.New("conn reset")}
	j := &Job{Q: f}
	_, err := j.Run(context.Background())
	if err == nil {
		t.Fatal("expected non-nil err")
	}
}

func TestRun_DeleteErr_Wrapped(t *testing.T) {
	f := &fakeQ{deleteErr: errors.New("conn reset")}
	j := &Job{Q: f}
	_, err := j.Run(context.Background())
	if err == nil {
		t.Fatal("expected non-nil err")
	}
}
