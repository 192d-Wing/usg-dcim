// Package dhcpdriftcheck is the Go port of Python's dhcp_drift_check
// arq function (worker.py:267). Walks every enabled DhcpServer once
// per tick and runs diff.DiffAllScopes against it; the orchestrator
// already writes last_diff_* per scope, so this job is the cron
// driver only — no per-scope logic lives here.
//
// Per-server failures are LOGGED and the loop continues. A transport
// failure to one Kea CA shouldn't block drift refresh on the rest of
// the fleet; the LIST endpoint's ?diff_status= filter and the
// push-drifted route read whatever this leaves on the rows.
//
// Deferred work — Python's dhcp_drift_check at worker.py:301 calls
// dhcp_alerts.notify_drift_transitions to fire Slack/email/webhook
// notifications for freshly-drifted scopes (Python PR 86), and at
// worker.py:314 populates Prometheus gauges (Python PR 97). Both
// depend on shared infra not yet ported to Go (the dhcp_alerts
// package + metrics registry). The transitions list this job already
// computes is the input the future alert dispatcher will consume; the
// wiring point is the per-server loop body, after diff.DiffAllScopes
// returns.
package dhcpdriftcheck

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/diff"
)

const Name = "dhcp_drift_check"

// Querier is the slim DB surface this job needs. ListEnabledDhcp
// ServerIDs walks the fleet (already present from the bundle-rerender
// job, PR 4 of the bundle series); diff.BulkQuerier embeds the
// per-scope diff Querier + ListAllScopeIDsAndPriorDriftForServer.
// `*dbq.Queries` satisfies both.
type Querier interface {
	ListEnabledDhcpServerIDs(ctx context.Context) ([]uuid.UUID, error)
	diff.BulkQuerier
}

// Job is the scheduler-side wrapper. KeaBuilder injects the per-server
// Kea client builder so tests can substitute a fake without standing
// up an HTTP server. Production wires diff.DefaultKeaClientBuilder.
// GetDhcpServerForPush is needed by DiffScope to load the per-server
// Kea URL + credentials; the orchestrator hands the row to KeaBuilder
// internally.
type Job struct {
	Q          Querier
	KeaBuilder diff.KeaClientBuilder
	Log        *slog.Logger
}

func (j *Job) Name() string { return Name }

// Run walks every enabled DhcpServer, runs DiffAllScopes, accumulates
// counters across servers. Returns {servers, errors, total_scopes,
// total_drifted, total_transitions} for the scheduler harness's
// structured log; per-server breakdowns are logged at INFO level
// inside the loop so the aggregate stays one line.
func (j *Job) Run(ctx context.Context) (map[string]any, error) {
	if j.Q == nil {
		return nil, errors.New("dhcpdriftcheck: Querier is nil")
	}
	build := j.KeaBuilder
	if build == nil {
		build = diff.DefaultKeaClientBuilder
	}
	logger := j.Log
	if logger == nil {
		logger = slog.Default()
	}
	serverIDs, err := j.Q.ListEnabledDhcpServerIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list enabled servers: %w", err)
	}
	servers, errCount, totalScopes, totalDrifted, totalTransitions := 0, 0, 0, 0, 0
	for _, id := range serverIDs {
		// Honor context cancellation between servers so a SIGTERM
		// mid-tick bails cleanly without firing per-server WARN
		// lines for every remaining ID. Returns the partial counts
		// so the scheduler harness's structured log still reflects
		// the work that completed. Same shape as the bundle-rerender
		// job's cancellation guard.
		if err := ctx.Err(); err != nil {
			return map[string]any{
				"servers":           servers,
				"errors":            errCount,
				"total_scopes":      totalScopes,
				"total_drifted":     totalDrifted,
				"total_transitions": totalTransitions,
			}, nil
		}
		servers++
		report, err := diff.DiffAllScopes(ctx, j.Q, build, id)
		if err != nil {
			// Graceful-drain cancellation surfaces from DiffAllScopes
			// as context.Canceled wrapped in "diff scope X: %w".
			// Bucketing this as a per-server failure would page
			// operators on every SIGTERM and inflate errCount in
			// the harness's structured log. Bail like the top-of-
			// loop ctx.Err guard and return partial counts so the
			// completed servers stay visible.
			if errors.Is(err, context.Canceled) {
				servers--
				return map[string]any{
					"servers":           servers,
					"errors":            errCount,
					"total_scopes":      totalScopes,
					"total_drifted":     totalDrifted,
					"total_transitions": totalTransitions,
				}, nil
			}
			errCount++
			logger.Warn("dhcp_drift_check_server_failed",
				"server_id", id, "err", err)
			continue
		}
		totalScopes += report.Total
		totalDrifted += report.Counts[string(diff.StatusDrifted)]
		totalTransitions += len(report.Transitions)
		// Per-server log line so operators can trace one server's
		// outcome without diff-walking the whole aggregate. The
		// transitions count surfaces newly-drifted scopes (cold
		// start + every actual change) for ops grep; the per-
		// transition prefix/scope_id detail lives in the bulk report
		// which a future alert dispatcher PR will consume.
		logger.Info("dhcp_drift_check_server_done",
			"server_id", id,
			"total", report.Total,
			"counts", report.Counts,
			"transitions", len(report.Transitions))
	}
	return map[string]any{
		"servers":           servers,
		"errors":            errCount,
		"total_scopes":      totalScopes,
		"total_drifted":     totalDrifted,
		"total_transitions": totalTransitions,
	}, nil
}

// Compile-time check: *dbq.Queries satisfies our Querier interface so
// a future sqlc regen that drops one of these methods fails at the
// change site instead of at link time. Same pattern as the bundle
// rerender job.
var _ Querier = (*dbq.Queries)(nil)
