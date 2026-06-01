// Go port of Python's bulk DHCP push surface (api/ipam.py:2517/2551/
// 2584). Three thin wrappers around the bulk orchestrators in
// internal/dhcp/push and internal/dhcp/diff:
//
//   POST /api/v1/ipam/dhcp/servers/{id}/scopes/push-all
//   POST /api/v1/ipam/dhcp/servers/{id}/scopes/push-drifted
//   GET  /api/v1/ipam/dhcp/servers/{id}/scopes/diff-all
//
// ABAC is enforced on the server's fabric_id (single GetDhcpServer
// FabricID lookup, already in the Querier from PR 54). Wire shapes
// are byte-identical to the Python responses so the dhcp-site UI
// and operator tooling cut over without a contract change.
package ipam

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/diff"
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/push"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

const errDhcpServerNotFoundBulk = "dhcp server not found"

// pushAllDhcpScopes mirrors api/ipam.py:2517. Pushes every enabled
// scope on the server serially. Per-scope failures don't fail the
// HTTP request — 200 + a results array with mixed statuses is the
// normal shape. The audit row carries the aggregate counts, not the
// full result set (the operator can re-read the response for that).
func (h *Handler) pushAllDhcpScopes(w http.ResponseWriter, r *http.Request) {
	h.runBulkPush(w, r, "dhcp_scope.push_all", push.PushAllScopes)
}

// pushDriftedDhcpScopes mirrors api/ipam.py:2551. Same shape as
// pushAllDhcpScopes — the only difference is which list query the
// orchestrator runs (drifted vs all-enabled). Operators should diff
// first so the cache is fresh; an empty drifted set returns a
// successful report with Total=0.
func (h *Handler) pushDriftedDhcpScopes(w http.ResponseWriter, r *http.Request) {
	h.runBulkPush(w, r, "dhcp_scope.push_drifted", push.PushDriftedScopes)
}

// runBulkPush is the shared body for push-all and push-drifted: ABAC
// on server fabric, run the orchestrator, audit the aggregate, emit
// the report. The action string + the orchestrator function are the
// only two things that differ between the two routes.
func (h *Handler) runBulkPush(
	w http.ResponseWriter, r *http.Request, action string,
	run func(context.Context, push.BulkQuerier, push.KeaClientBuilder, uuid.UUID) (push.BulkReport, error),
) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if !h.enforceDhcpServerFabric(w, r, id, "ipam:dhcp-scopes:push") {
		return
	}
	report, err := run(r.Context(), h.Q, h.pushKeaBuilder(), id)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	// Audit metadata carries `total` + `counts` so the operator can
	// see the batch summary in the audit log without grepping the
	// per-scope rows. Matches services/dhcp_push handlers at lines
	// 2537/2573 for push_all/push_drifted respectively.
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: action, TargetType: "dhcp_server", TargetID: id.String(),
		Metadata: map[string]any{
			"total":  report.Total,
			"counts": report.Counts,
		},
	})
	httpx.JSON(w, http.StatusOK, newBulkPushBody(report))
}

// diffAllDhcpScopes mirrors api/ipam.py:2584. Drift-checks every
// scope on the server (including disabled). No audit row — Python
// also skips it (the operation is a read, the persist side-effect
// is per-scope and already recorded in last_diff_at).
func (h *Handler) diffAllDhcpScopes(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if !h.enforceDhcpServerFabric(w, r, id, "ipam:dhcp-scopes:read") {
		return
	}
	report, err := diff.DiffAllScopes(r.Context(), h.Q, h.diffKeaBuilder(), id)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, newBulkDiffBody(report))
}

// enforceDhcpServerFabric runs the single-hop server → fabric lookup
// (GetDhcpServerFabricID, already in PR 54's ABAC family) and the
// EnforceFabricScope check. Distinct from enforceDhcpScopeFabric
// (which runs a 2-hop scope → server → fabric join) because the
// bulk endpoints' URL carries server_id directly.
func (h *Handler) enforceDhcpServerFabric(w http.ResponseWriter, r *http.Request, serverID uuid.UUID, capCode string) bool {
	fid, ok := h.lookupFabricID(w, r.Context(),
		func(ctx context.Context) (uuid.UUID, error) { return h.Q.GetDhcpServerFabricID(ctx, serverID) },
		errDhcpServerNotFoundBulk)
	if !ok {
		return false
	}
	return h.enforceFabric(w, r, fid, capCode)
}

// bulkPushBody mirrors Python's push-all/push-drifted response dict
// at api/ipam.py:2543/2576. Results carries the per-scope push
// results in the same wire shape as the per-scope endpoint.
type bulkPushBody struct {
	ServerID string           `json:"server_id"`
	Total    int              `json:"total"`
	Counts   map[string]int   `json:"counts"`
	Results  []pushResultBody `json:"results"`
}

func newBulkPushBody(r push.BulkReport) bulkPushBody {
	results := make([]pushResultBody, len(r.Results))
	for i, x := range r.Results {
		results[i] = newPushResultBody(x)
	}
	return bulkPushBody{
		ServerID: r.ServerID,
		Total:    r.Total,
		Counts:   r.Counts,
		Results:  results,
	}
}

// bulkDiffBody mirrors api/ipam.py:2603. PR 86 added the transitions
// list — Python's BulkDiffReport carries it but the response handler
// at line 2603 does NOT serialize it (the field exists for cron
// consumers, not API consumers). Mirror that posture: keep the data
// available on diff.BulkReport but omit it from the API body.
type bulkDiffBody struct {
	ServerID string           `json:"server_id"`
	Total    int              `json:"total"`
	Counts   map[string]int   `json:"counts"`
	Results  []diffResultBody `json:"results"`
}

func newBulkDiffBody(r diff.BulkReport) bulkDiffBody {
	results := make([]diffResultBody, len(r.Results))
	for i, x := range r.Results {
		results[i] = newDiffResultBody(x)
	}
	return bulkDiffBody{
		ServerID: r.ServerID,
		Total:    r.Total,
		Counts:   r.Counts,
		Results:  results,
	}
}
