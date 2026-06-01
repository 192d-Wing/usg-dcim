// Bulk push orchestrators — push-all + push-drifted.
//
// Both are thin wrappers around the per-scope PushScope from push.go:
// list the relevant scope ids, iterate serially, aggregate results.
// Serial (not parallel) for the same reason Python does
// (services/dhcp_push.py:1033): AllocateKeaSubnetID reads the live
// kea_subnet_id set to pick the next free integer, so two concurrent
// first-pushes could both pick id=1 and conflict on Kea's side. A
// per-server mutex around AllocateKeaSubnetID would let the pushes
// fan out, but at ~tens of scopes per server the serial loop is fine
// and matches Python's posture.
package push

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/usg-dcim/packages/otter-go/internal/dhcp/kea"
)

// pushStatuses is the fixed-key tally the BulkPushReport carries. An
// "other" bucket appears only if a future status value drifts in
// without being added here — same as Python's _tally fallback at
// services/dhcp_push.py:984.
var pushStatuses = []kea.Status{kea.StatusOK, kea.StatusError, kea.StatusUnsupported}

// BulkQuerier is the slim DB surface push-all / push-drifted need
// beyond the per-scope Querier. ListEnabledScopeIDsForServer drives
// push-all; ListDriftedScopeIDsForServer drives push-drifted.
type BulkQuerier interface {
	Querier
	ListEnabledScopeIDsForServer(ctx context.Context, dhcpServerID uuid.UUID) ([]uuid.UUID, error)
	ListDriftedScopeIDsForServer(ctx context.Context, dhcpServerID uuid.UUID) ([]uuid.UUID, error)
}

// BulkReport is the per-batch return shape. Mirrors Python's
// BulkPushReport dataclass (services/dhcp_push.py:955). ServerID is
// stringified to match Python's `str(server.id)`.
type BulkReport struct {
	ServerID string
	Total    int
	Counts   map[string]int
	Results  []Result
}

// PushAllScopes pushes every enabled, non-deleted scope on the
// server serially. Per-scope failures do not abort the batch — each
// failure shows up as a Result with Status=error/unsupported in the
// returned slice; the caller surfaces them to the operator.
//
// Returns a non-nil error only when an internal step (list query or
// the orchestrator's "fatal" branch) fails. Per-scope pushes that
// surface as Result.Status="error" are NOT errors at this level —
// Python treats them the same way (line 1056).
func PushAllScopes(ctx context.Context, q BulkQuerier, build KeaClientBuilder, serverID uuid.UUID) (BulkReport, error) {
	ids, err := q.ListEnabledScopeIDsForServer(ctx, serverID)
	if err != nil {
		return BulkReport{}, fmt.Errorf("list enabled scopes: %w", err)
	}
	return runBulkPush(ctx, q, build, serverID, ids)
}

// PushDriftedScopes pushes only scopes whose persisted drift status
// is 'drifted'. The empty-set case (no scopes drifted, or drift
// cache stale) returns a report with Total=0 and zero counts —
// matching Python's posture, the operator can act on the empty
// report by running a fresh diff pass first.
func PushDriftedScopes(ctx context.Context, q BulkQuerier, build KeaClientBuilder, serverID uuid.UUID) (BulkReport, error) {
	ids, err := q.ListDriftedScopeIDsForServer(ctx, serverID)
	if err != nil {
		return BulkReport{}, fmt.Errorf("list drifted scopes: %w", err)
	}
	return runBulkPush(ctx, q, build, serverID, ids)
}

// runBulkPush is the shared loop: per-scope PushScope, append result,
// aggregate counts at the end. results is preallocated because Total
// is known up-front.
func runBulkPush(ctx context.Context, q Querier, build KeaClientBuilder, serverID uuid.UUID, ids []uuid.UUID) (BulkReport, error) {
	results := make([]Result, 0, len(ids))
	for _, id := range ids {
		r, err := PushScope(ctx, q, build, id)
		if err != nil {
			// PushScope only returns a non-nil err for unexpected
			// internal failures (DB unreachable, template fetch
			// errored). Bail the batch — staying in the loop would
			// log misleading "per-scope error" rows for what is
			// really infrastructure failure.
			return BulkReport{}, fmt.Errorf("push scope %s: %w", id, err)
		}
		results = append(results, r)
	}
	return BulkReport{
		ServerID: serverID.String(),
		Total:    len(results),
		Counts:   tallyPushStatuses(results),
		Results:  results,
	}, nil
}

// tallyPushStatuses aggregates observed statuses into the fixed-key
// count map. Unknown statuses (shouldn't happen) land in "other" so
// the operator notices them — same fallback Python uses at
// services/dhcp_push.py:996.
func tallyPushStatuses(results []Result) map[string]int {
	counts := make(map[string]int, len(pushStatuses))
	for _, s := range pushStatuses {
		counts[string(s)] = 0
	}
	other := 0
	for _, r := range results {
		k := string(r.Status)
		if _, ok := counts[k]; ok {
			counts[k]++
		} else {
			other++
		}
	}
	if other > 0 {
		counts["other"] = other
	}
	return counts
}
