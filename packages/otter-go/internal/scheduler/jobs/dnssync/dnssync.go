// Package dnssync is the Go port of Python's dns_sync_from_ipam arq
// cron in packages/otter/src/dcim/worker.py:351. Every 5 minutes it
// rebuilds source=ipam/ddns DNS records for every kind=site zone +
// derived reverse zones, catching IPAM allocations / DHCP leases that
// landed since the last cycle. Apex zones are skipped (operator-
// curated; the per-zone helper short-circuits on non-site kinds).
//
// All the heavy lifting (forward+reverse emission, reverse-zone auto-
// create on demand at /24 (v4) or /64 (v6) boundaries, projected-record
// DELETE+INSERT) lives in internal/dns.SyncIPAMRecordsForZone — the
// same code path the user-triggered POST /dns/zones/{id}/sync-from-ipam
// endpoint calls. This job is the cron-tier loop wrapper.
package dnssync

import (
	"context"
	"errors"
	"fmt"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/dns"
)

const Name = "dns_sync_from_ipam"

// Querier is the slim DB surface this job needs. *dbq.Queries
// satisfies it; tests substitute a fake. Embeds dns.SyncQuerier
// because every zone-level call goes through that helper.
type Querier interface {
	dns.SyncQuerier
	ListAllSiteDnsZones(ctx context.Context) ([]dbq.DnsZone, error)
}

type Job struct {
	Q Querier
}

func (j *Job) Name() string { return Name }

// Run lists every site zone and runs the IPAM projection once per
// zone. Per-zone failures abort the whole sweep (matching Python's
// `for z in zones: await dns_svc.sync_ipam_records_for_zone(...)`
// which has no try/except — one bad zone fails the cron tick). The
// scheduler will log and retry on the next 5-minute boundary; the
// sync helper is idempotent (DELETE+rebuild) so a partial run from
// the failing tick is healed on retry.
func (j *Job) Run(ctx context.Context) (map[string]any, error) {
	if j.Q == nil {
		return nil, errors.New("dnssync: Querier is nil")
	}
	zones, err := j.Q.ListAllSiteDnsZones(ctx)
	if err != nil {
		return nil, fmt.Errorf("list site zones: %w", err)
	}
	var totalAdded, totalRemoved int
	for _, z := range zones {
		added, removed, err := dns.SyncIPAMRecordsForZone(ctx, j.Q, z)
		if err != nil {
			return nil, fmt.Errorf("zone %s (%s): %w", z.ID, z.Name, err)
		}
		totalAdded += added
		totalRemoved += removed
	}
	return map[string]any{
		"added":   totalAdded,
		"removed": totalRemoved,
		"zones":   len(zones),
	}, nil
}
