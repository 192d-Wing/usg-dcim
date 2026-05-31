// Package dnspurge is the Go port of Python's dns_purge_metrics cron
// job in packages/otter/src/dcim/worker.py. It drops every row from
// dns_server_metrics_samples with observed_at older than the
// configured retention window — the table grows unbounded otherwise
// (every dns-collector scrape inserts a fresh row).
//
// Default retention is 30 days, matching Python's
// settings.dns_metrics_retention_days default. Operators override via
// DCIM_DNS_METRICS_RETENTION_DAYS at the otter-go-scheduler binary.
package dnspurge

import (
	"context"
	"errors"
	"time"
)

const Name = "dns_purge_metrics"

// Querier is the slim interface this job needs. *dbq.Queries
// satisfies it; tests substitute a fake that records the cutoff
// without touching Postgres.
type Querier interface {
	DeleteDnsServerMetricsSamplesOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
}

// Job is the scheduler.Job implementation. RetentionDays is the
// number of days to keep. Now lets tests pin the clock; production
// passes nil to use time.Now.
type Job struct {
	Q             Querier
	RetentionDays int
	Now           func() time.Time
}

// scheduler.Job interface compliance.
func (j *Job) Name() string { return Name }

func (j *Job) Run(ctx context.Context) (map[string]any, error) {
	if j.Q == nil {
		return nil, errors.New("dnspurge: Querier is nil")
	}
	if j.RetentionDays < 1 {
		return nil, errors.New("dnspurge: RetentionDays must be >= 1")
	}
	now := time.Now
	if j.Now != nil {
		now = j.Now
	}
	cutoff := now().Add(-time.Duration(j.RetentionDays) * 24 * time.Hour)
	deleted, err := j.Q.DeleteDnsServerMetricsSamplesOlderThan(ctx, cutoff)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"deleted":        deleted,
		"retention_days": j.RetentionDays,
		"cutoff":         cutoff.Format(time.RFC3339),
	}, nil
}
