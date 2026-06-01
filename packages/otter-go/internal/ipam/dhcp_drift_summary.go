// Go port of Python's GET /api/v1/ipam/dhcp/drift-summary handler
// (api/ipam.py:2611). Fleet-wide DHCP drift aggregation. Three
// SELECTs (servers in scope, scopes by server, firing dhcp-drift
// alert keys) feed the pure aggregator in internal/dhcp/driftsummary.
//
// ABAC: ScopedFabricFilter on ipam:dhcp-scopes:read (same gate
// list_dhcp_servers uses, matching Python at line 2628). An empty
// in-scope set short-circuits to the empty-fleet shape — no DB hits
// for a scope-deny caller.
package ipam

import (
	"net/http"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/driftsummary"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

func (h *Handler) dhcpDriftSummary(w http.ResponseWriter, r *http.Request) {
	scopeIds, ok := scopedListFilter(r, "ipam:dhcp-scopes:read")
	if !ok {
		// No-scope short-circuit. Python's body at api/ipam.py:2632-
		// 2645 emits ONLY `fleet` + `servers` — the `fabrics` key is
		// absent. Replicating that exactly so a consumer using
		// `"fabrics" in payload` to discriminate populated vs.
		// no-scope paths keeps the same behavior across backends.
		httpx.JSON(w, http.StatusOK, dhcpDriftSummaryNoScopeBody{
			Fleet:   driftsummary.EmptyFleetSummary(),
			Servers: []driftsummary.ServerSummary{},
		})
		return
	}

	servers, err := h.Q.ListDhcpServersForDriftSummary(r.Context(), scopeIds)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if len(servers) == 0 {
		// Python's second short-circuit (no servers in scope at all)
		// returns the full empty shape WITH the `fabrics` key set to
		// []. Distinct from the no-scope branch above.
		httpx.JSON(w, http.StatusOK, dhcpDriftSummaryBody{
			Fleet:   driftsummary.EmptyFleetSummary(),
			Fabrics: []driftsummary.FabricSummary{},
			Servers: []driftsummary.ServerSummary{},
		})
		return
	}

	serverIDs := make([]uuid.UUID, len(servers))
	for i, s := range servers {
		serverIDs[i] = s.ID
	}
	scopes, err := h.Q.ListDhcpScopeDriftStatusByServers(r.Context(), serverIDs)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	alertKeys, err := h.Q.ListFiringDhcpDriftAlertKeys(r.Context())
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}

	fleet, fabrics, perServer := driftsummary.Aggregate(
		servers,
		driftsummary.ScopesByServer(scopes),
		driftsummary.AlertCountsByScopeID(alertKeys),
	)
	httpx.JSON(w, http.StatusOK, dhcpDriftSummaryBody{
		Fleet: fleet, Fabrics: fabrics, Servers: perServer,
	})
}

// dhcpDriftSummaryBody mirrors Python's top-level response dict at
// api/ipam.py:2693-2728. The three nested objects come straight
// from the aggregator's JSON-tagged structs.
type dhcpDriftSummaryBody struct {
	Fleet   driftsummary.FleetSummary    `json:"fleet"`
	Fabrics []driftsummary.FabricSummary `json:"fabrics"`
	Servers []driftsummary.ServerSummary `json:"servers"`
}

// dhcpDriftSummaryNoScopeBody is the narrower shape Python's
// no-scope branch emits (api/ipam.py:2632-2645 — `fabrics` key is
// intentionally absent). Modeled as a separate struct so the
// `omitempty` route would have falsely emitted `"fabrics":[]` on
// the populated path too.
type dhcpDriftSummaryNoScopeBody struct {
	Fleet   driftsummary.FleetSummary    `json:"fleet"`
	Servers []driftsummary.ServerSummary `json:"servers"`
}

// Compile-time check that *dbq.Queries satisfies the ipam.Querier
// interface. Catches a future sqlc regen that drops or renames any
// of the methods the handler depends on — the breakage surfaces in
// this package instead of at link time in cmd/otter-go/main.go.
// Same pattern as internal/dhcp/diff/diff.go's `var _ Querier = ...`.
var _ Querier = (*dbq.Queries)(nil)
