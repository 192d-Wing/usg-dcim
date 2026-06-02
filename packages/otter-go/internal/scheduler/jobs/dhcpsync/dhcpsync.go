// Package dhcpsync is the Go port of Python's dhcp_sync arq cron
// (worker.py:73 → services/kea.py:sync_all_servers). Walks every
// enabled DhcpServer once per tick and runs leasesync.SyncServer
// against each.
//
// Per-server failures are LOGGED and the loop continues. The
// orchestrator (PR 15) already records the failure on the
// DhcpServer row (last_sync_status='error' + truncated message), so
// the cron driver doesn't need to keep extra state — operators see
// the per-server status in the LIST endpoint regardless.
//
// Result map shape divergence from Python:
//   Python sync_all_servers (services/kea.py:351-356) returns:
//     {servers, upserted, skipped_no_subnet, errors: [list of msgs]}
//   Go returns:
//     {servers, errors: int, total_upserted, total_skipped_no_subnet,
//      total_leases_seen}
// Three deliberate changes: (a) "errors" is a count not a list —
// the per-server message lives in dhcp_servers.last_sync_error which
// is the canonical source the LIST endpoint surfaces; (b) totals are
// "total_" prefixed for clarity that they're fleet-wide sums; (c)
// "total_leases_seen" is new — surfaces the parse-vs-skip count
// Python tossed. Cutover dashboards keying on the old names need
// updating.
package dhcpsync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/leasesync"
)

const Name = "dhcp_sync"

// Querier is the slim DB surface this job needs. ListEnabledDhcpServers
// ForLeaseSync iterates the fleet; the embedded leasesync.Querier
// powers per-server SyncServer calls. *dbq.Queries satisfies both.
type Querier interface {
	ListEnabledDhcpServersForLeaseSync(ctx context.Context) ([]dbq.DhcpServerForLeaseSyncRow, error)
	leasesync.Querier
}

// Job wires the production Querier + KeaBuilder + logger. KeaBuilder
// defaults to leasesync.DefaultKeaClientBuilder when nil; the cron
// rarely needs an override (tests do).
type Job struct {
	Q          Querier
	KeaBuilder leasesync.KeaClientBuilder
	Log        *slog.Logger
	// Now exists for tests so the per-server SyncServer timestamps
	// are deterministic. Production leaves it nil → time.Now.
	Now func() time.Time
}

func (j *Job) Name() string { return Name }

// Run walks every enabled DhcpServer. Returns counters for the
// harness's structured log: servers walked, total leases upserted
// across the fleet, total skipped_no_subnet, count of servers that
// reported a Kea transport error. Per-server failure detail lives
// in the LIST endpoint via dhcp_servers.last_sync_error.
func (j *Job) Run(ctx context.Context) (map[string]any, error) {
	if j.Q == nil {
		return nil, errors.New("dhcpsync: Querier is nil")
	}
	build := j.KeaBuilder
	if build == nil {
		build = leasesync.DefaultKeaClientBuilder
	}
	logger := j.Log
	if logger == nil {
		logger = slog.Default()
	}
	now := time.Now
	if j.Now != nil {
		now = j.Now
	}
	servers, err := j.Q.ListEnabledDhcpServersForLeaseSync(ctx)
	if err != nil {
		return nil, fmt.Errorf("list enabled servers: %w", err)
	}

	var (
		walked, errCount    int
		totalUpserted       int
		totalSkippedNoSubnet int
		totalSeen           int
	)
	for _, row := range servers {
		// Honor context cancellation between servers so a SIGTERM
		// mid-tick on a many-server fleet bails cleanly. Returns the
		// partial counts so the harness log still reflects work that
		// completed. Same pattern dhcpbundle/dhcpdriftcheck use.
		if err := ctx.Err(); err != nil {
			return summary(walked, errCount, totalUpserted, totalSkippedNoSubnet, totalSeen), nil
		}
		walked++
		server := toServer(row)
		result, err := leasesync.SyncServer(ctx, j.Q, build, server, now())
		if err != nil {
			// SyncServer returns non-nil err only for unexpected
			// internal failures (DB unreachable inside the
			// orchestrator). The cron driver logs and continues —
			// matches dhcpdriftcheck's posture.
			errCount++
			logger.Warn("dhcp_sync_server_failed",
				"server_id", row.ID, "err", err)
			continue
		}
		if result.Error != "" {
			// Kea transport failure: the orchestrator already wrote
			// last_sync_status='error' on the row. Count it and move
			// on; don't treat it as a fatal error at this layer.
			// Truncate the logged err preview — a verbose Kea TLS
			// stack trace shouldn't balloon the structured-log
			// volume per tick (the DB column already carries the
			// full 2000-char message).
			errCount++
			logger.Warn("dhcp_sync_server_kea_error",
				"server_id", row.ID, "err", logPreview(result.Error))
			continue
		}
		totalUpserted += result.Upserted
		totalSkippedNoSubnet += result.SkippedNoSubnet
		totalSeen += result.LeasesSeen
		logger.Info("dhcp_sync_server_done",
			"server_id", row.ID, "upserted", result.Upserted,
			"skipped_no_subnet", result.SkippedNoSubnet, "leases_seen", result.LeasesSeen)
	}
	return summary(walked, errCount, totalUpserted, totalSkippedNoSubnet, totalSeen), nil
}

// summary is the result map shape both the success path and the
// ctx-cancel bail use. Extracted so the two return sites stay in sync.
func summary(walked, errCount, upserted, skippedNoSubnet, seen int) map[string]any {
	return map[string]any{
		"servers":           walked,
		"errors":            errCount,
		"total_upserted":    upserted,
		"total_skipped_no_subnet": skippedNoSubnet,
		"total_leases_seen": seen,
	}
}

// logPreview clips an error message to a sane log-line length. The
// DB column already stores the full 2000-char truncated form via
// recordSyncFailure; the cron's per-server log line just needs
// enough to triage, not the entire stack trace a verbose Kea TLS
// failure might carry. Same rune-safe shape as leasesync's
// truncateRuneSafe — applied here for symmetry without exporting it.
func logPreview(s string) string {
	const max = 256
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "...(truncated)"
}

// toServer maps the row projection to the leasesync.Server input.
// Pointer-to-string fields fall back to empty string — the Kea client
// uses the empty pair as "unauthenticated" (services/kea.py:120).
func toServer(row dbq.DhcpServerForLeaseSyncRow) leasesync.Server {
	user, pass := "", ""
	if row.AuthUsername != nil {
		user = *row.AuthUsername
	}
	if row.AuthPassword != nil {
		pass = *row.AuthPassword
	}
	return leasesync.Server{
		ID: row.ID, FabricID: row.FabricID,
		KeaURL: row.KeaURL, AuthUsername: user, AuthPassword: pass,
	}
}

// Compile-time check: *dbq.Queries satisfies the Querier surface. A
// sqlc regen that drops one of the embedded queries fails here, not
// at link time in cmd/otter-go-scheduler/main.go.
var _ Querier = (*dbq.Queries)(nil)
