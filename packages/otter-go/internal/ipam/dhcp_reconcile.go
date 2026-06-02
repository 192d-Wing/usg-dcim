// Go port of Python's GET /api/v1/ipam/dhcp/scopes/{id}/reconcile
// (api/ipam.py:2268). Cross-checks the scope's reservations against
// IPAM. Read-only: pure report, no DB or Kea mutations.
//
// Two SELECTs: GetDhcpScope (the row, including reservations_json +
// subnet_id), then ListIPAddressesInSubnetForReconcile against that
// subnet. The pure aggregator in internal/dhcp/reconcile does the
// classification.
//
// Capability is ipam:dhcp-scopes:reconcile (distinct from :read and
// :update so operators can grant the cross-check view to one team
// and the mutating sync — PR 13 — to another).
package ipam

import (
	"net/http"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/reconcile"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

func (h *Handler) reconcileDhcpScope(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if !h.enforceDhcpScopeFabric(w, r, id, "ipam:dhcp-scopes:reconcile") {
		return
	}
	scope, err := h.Q.GetDhcpScope(r.Context(), id)
	if err != nil {
		mapErr(w, err, errDhcpScopeNotFoundCRUD)
		return
	}
	// Python at lines 2291-2297: only load IPAddress rows when the
	// scope HAS a subnet_id. The aggregator handles nil subnet too
	// (every reservation falls into unbacked with an explanatory
	// note), but skipping the SELECT keeps the cron-style polling
	// pattern cheap on never-linked scopes.
	var ipRows []dbq.DhcpReconcileIPRow
	if scope.SubnetID != nil {
		rows, err := h.Q.ListIPAddressesInSubnetForReconcile(r.Context(), *scope.SubnetID)
		if err != nil {
			status, msg := httpx.Mapped(err)
			httpx.Error(w, status, msg)
			return
		}
		ipRows = rows
	}
	report := reconcile.Reconcile(scope.ID, scope.SubnetID, scope.ReservationsJSON, ipRows)
	httpx.JSON(w, http.StatusOK, report)
}
