// Package dhcptombstone is the Go port of Python's
// dhcp_scope_tombstone_purge arq cron (worker.py:141). Once a day at
// 03:30 it hard-deletes soft-deleted dhcp_scopes rows whose
// deleted_at is older than the retention window — the live Kea-side
// DELETE already ran when the operator soft-deleted the scope, so
// this only drops orphaned tombstone rows from Postgres.
//
// No Kea integration is reached: the Python original explicitly
// documents this ("No Kea-side action: the original DELETE already
// removed the subnet from Kea"). The cron is a single SQL DELETE.
package dhcptombstone

import (
	"context"
	"errors"
	"time"
)

const Name = "dhcp_scope_tombstone_purge"

// Querier is the slim DB surface this job needs.
type Querier interface {
	PurgeExpiredDhcpScopeTombstones(ctx context.Context, cutoff time.Time) (int64, error)
}

type Job struct {
	Q             Querier
	RetentionDays int
	// Now exists for tests; production leaves it nil → time.Now.
	Now func() time.Time
}

func (j *Job) Name() string { return Name }

func (j *Job) Run(ctx context.Context) (map[string]any, error) {
	if j.Q == nil {
		return nil, errors.New("dhcptombstone: Querier is nil")
	}
	if j.RetentionDays <= 0 {
		return nil, errors.New("dhcptombstone: RetentionDays must be > 0")
	}
	now := time.Now
	if j.Now != nil {
		now = j.Now
	}
	cutoff := now().UTC().Add(-time.Duration(j.RetentionDays) * 24 * time.Hour)
	purged, err := j.Q.PurgeExpiredDhcpScopeTombstones(ctx, cutoff)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"purged":         purged,
		"cutoff":         cutoff.Format(time.RFC3339),
		"retention_days": j.RetentionDays,
	}, nil
}
