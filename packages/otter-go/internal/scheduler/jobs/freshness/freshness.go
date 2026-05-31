// Package freshness is the Go port of Python's freshness_sweep cron
// job in packages/otter/src/dcim/worker.py:55. Every 5 minutes it
// flips telemetry_sources rows from current → stale when their last
// successful poll is older than max(60s, poll_interval_seconds * 3).
//
// Python loads every row in-memory and loops; the Go port pushes the
// comparison into a single UPDATE statement (FlipStaleTelemetrySources
// in db/queries/telemetry.sql) so the job stays constant-memory even
// on a fleet with 10k+ sources.
package freshness

import (
	"context"
	"errors"
)

const Name = "freshness_sweep"

// Querier is the slim interface this job needs. *dbq.Queries
// satisfies it; tests substitute a fake that records the call.
type Querier interface {
	FlipStaleTelemetrySources(ctx context.Context) (int64, error)
}

type Job struct {
	Q Querier
}

func (j *Job) Name() string { return Name }

func (j *Job) Run(ctx context.Context) (map[string]any, error) {
	if j.Q == nil {
		return nil, errors.New("freshness: Querier is nil")
	}
	flipped, err := j.Q.FlipStaleTelemetrySources(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"flipped": flipped}, nil
}
