// Package dhcpageout is the Go port of Python's dhcp_age_out arq
// cron (worker.py:78 → services/kea.py:age_out_stale_dhcp).
//
// Two SQL passes:
//
//   1. DeprecateExpiredDhcpLeases — flip status from active to
//      deprecated for source=dhcp rows whose lease lapsed > grace_
//      seconds ago. Static + reservation rows are untouched.
//   2. DeleteDeprecatedDhcpLeases — hard-delete source=dhcp rows
//      that have been deprecated > 1 day (Python's `cutoff_delete`).
//
// Pure SQL — no per-row iteration on the Go side, identical to
// Python's posture at services/kea.py:362-402 (Python iterates in
// Python, Go pushes the predicate into the WHERE so the DB does it
// in one pass).
//
// Result map shape divergence from Python: Python returns
// `{aged_out: total}` (single int). Go returns
// `{deprecated, deleted, grace_seconds, delete_after_days,
// cutoff_deprecate, cutoff_delete}` — splits the two passes and
// surfaces the config + cutoffs so cron-run forensics don't need
// to grep the binary's env for the values that were active. Cutover
// dashboards keying on `aged_out` need updating to read
// `deprecated + deleted`.
package dhcpageout

import (
	"context"
	"errors"
	"time"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

const Name = "dhcp_age_out"

// defaultGraceSeconds matches Python's services/kea.py:362-363:
// `grace_seconds: int = 3600`. A lease whose expiry crossed in the
// last hour gets a benefit-of-the-doubt window before being
// deprecated — covers Kea's lease renewal jitter.
const defaultGraceSeconds = 3600

// defaultDeleteAfterDays is the Python `cutoff_delete = now -
// timedelta(days=1)` window at services/kea.py:370. Deprecated rows
// older than 24h are noise.
const defaultDeleteAfterDays = 1

// Querier is the slim DB surface this job needs.
type Querier interface {
	DeprecateExpiredDhcpLeases(ctx context.Context, cutoff time.Time) (int64, error)
	DeleteDeprecatedDhcpLeases(ctx context.Context, cutoff time.Time) (int64, error)
}

// Job wires the production Querier. GraceSeconds + DeleteAfterDays
// default to Python's values when zero — pass non-zero values for
// staging environments or short-lived test runs.
type Job struct {
	Q               Querier
	GraceSeconds    int
	DeleteAfterDays int
	// Now exists for tests so the cutoffs are deterministic.
	Now func() time.Time
}

func (j *Job) Name() string { return Name }

// Run executes the two SQL passes. Returns the per-pass row counts
// for the harness's structured log.
func (j *Job) Run(ctx context.Context) (map[string]any, error) {
	if j.Q == nil {
		return nil, errors.New("dhcpageout: Querier is nil")
	}
	grace := j.GraceSeconds
	if grace <= 0 {
		grace = defaultGraceSeconds
	}
	delAfter := j.DeleteAfterDays
	if delAfter <= 0 {
		delAfter = defaultDeleteAfterDays
	}
	nowFn := time.Now
	if j.Now != nil {
		nowFn = j.Now
	}
	now := nowFn().UTC()
	cutoffDeprecate := now.Add(-time.Duration(grace) * time.Second)
	cutoffDelete := now.Add(-time.Duration(delAfter) * 24 * time.Hour)

	deprecated, err := j.Q.DeprecateExpiredDhcpLeases(ctx, cutoffDeprecate)
	if err != nil {
		return nil, err
	}
	deleted, err := j.Q.DeleteDeprecatedDhcpLeases(ctx, cutoffDelete)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"deprecated":         deprecated,
		"deleted":            deleted,
		"grace_seconds":      grace,
		"delete_after_days":  delAfter,
		"cutoff_deprecate":   cutoffDeprecate.Format(time.RFC3339),
		"cutoff_delete":      cutoffDelete.Format(time.RFC3339),
	}, nil
}

// Compile-time check.
var _ Querier = (*dbq.Queries)(nil)
