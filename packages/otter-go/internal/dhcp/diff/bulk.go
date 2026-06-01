// Bulk diff orchestrator — diff-all.
//
// Drift-checks every scope on a server INCLUDING disabled ones (drift
// on a disabled scope is still informational — operator may have
// flipped enabled=False locally while Kea still serves it). Each
// per-scope result is persisted via WriteDhcpScopeDiffState so the
// LIST endpoint and push-drifted see fresh data on the next request.
// Serial like push-all because DiffScope's underlying Querier reads
// + writes are non-transactional and we want predictable persist
// ordering.
package diff

import (
	"context"
	"fmt"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/google/uuid"
)

// diffStatuses pins the fixed-key tally the BulkDiffReport carries.
// Adding a new Status value here keeps the count map's key set
// stable across cron runs so dashboards don't drop the new bucket.
var diffStatuses = []Status{
	StatusInSync, StatusDrifted, StatusMissingFromKea, StatusNeverPushed, StatusError,
}

// BulkQuerier is the slim DB surface diff-all needs beyond the
// per-scope Querier. ListAllScopeIDsAndPriorDriftForServer returns
// (id, prior_status) per scope so the loop can record transitions
// before WriteDhcpScopeDiffState overwrites last_diff_status.
type BulkQuerier interface {
	Querier
	ListAllScopeIDsAndPriorDriftForServer(ctx context.Context, dhcpServerID uuid.UUID) ([]dbq.DhcpScopeIDAndPriorDriftRow, error)
}

// Transition captures one scope's status change. Empty list on cold
// start means every scope was already at its final state from a
// prior pass — first-ever run treats every result as a transition
// (None → status) since prior was NULL.
type Transition struct {
	ScopeID    string  `json:"scope_id"`
	Prefix     string  `json:"prefix"`
	FromStatus *string `json:"from_status"`
	ToStatus   string  `json:"to_status"`
}

// BulkReport mirrors Python's BulkDiffReport (services/dhcp_push.py:963).
type BulkReport struct {
	ServerID    string
	Total       int
	Counts      map[string]int
	Results     []Result
	Transitions []Transition
}

// DiffAllScopes drift-checks every scope on the server.
//
// The list query projects (id, prefix, last_diff_status) so the loop
// can snapshot the prior status + the scope prefix BEFORE
// PersistDiffState overwrites it. Comparing the snapshot against the
// new Result.Status yields the transition list — empty when every
// scope is at its previous status, populated on first run (every
// scope transitions from NULL → some status). Prefix is sourced
// from the row, not from Result.DCIMSubnet, because the never_pushed
// short-circuit leaves DCIMSubnet nil; Python sources prefix from
// scope.prefix directly (services/dhcp_push.py:1111) so cold-start
// transitions still carry the prefix operators key off.
func DiffAllScopes(ctx context.Context, q BulkQuerier, build KeaClientBuilder, serverID uuid.UUID) (BulkReport, error) {
	rows, err := q.ListAllScopeIDsAndPriorDriftForServer(ctx, serverID)
	if err != nil {
		return BulkReport{}, fmt.Errorf("list all scopes: %w", err)
	}
	results := make([]Result, 0, len(rows))
	transitions := make([]Transition, 0)
	for _, row := range rows {
		result, err := DiffScope(ctx, q, build, row.ID)
		if err != nil {
			return BulkReport{}, fmt.Errorf("diff scope %s: %w", row.ID, err)
		}
		if err := PersistDiffState(ctx, q, result); err != nil {
			return BulkReport{}, fmt.Errorf("persist diff state %s: %w", row.ID, err)
		}
		if !statusEqual(row.LastDiffStatus, string(result.Status)) {
			transitions = append(transitions, Transition{
				ScopeID:    result.ScopeID.String(),
				Prefix:     row.Prefix,
				FromStatus: row.LastDiffStatus,
				ToStatus:   string(result.Status),
			})
		}
		results = append(results, result)
	}
	return BulkReport{
		ServerID:    serverID.String(),
		Total:       len(results),
		Counts:      tallyDiffStatuses(results),
		Results:     results,
		Transitions: transitions,
	}, nil
}

// statusEqual treats Python's `None != "in_sync"` semantics: a NULL
// prior status is a transition against any non-empty new status. Two
// equal non-nil strings are equal; one-side-nil is always unequal;
// two NULLs is equal (shouldn't happen but defensive).
func statusEqual(prior *string, current string) bool {
	if prior == nil {
		return current == ""
	}
	return *prior == current
}

// tallyDiffStatuses aggregates observed statuses into the fixed-key
// count map. Mirrors tallyPushStatuses in push/bulk.go (same
// algorithm; both packages keep their own copy so neither depends on
// the other's tally).
func tallyDiffStatuses(results []Result) map[string]int {
	counts := make(map[string]int, len(diffStatuses))
	for _, s := range diffStatuses {
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
