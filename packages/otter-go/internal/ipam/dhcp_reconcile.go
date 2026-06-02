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
	"github.com/usg-dcim/packages/otter-go/internal/audit"
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

// reconcileSyncDhcpScope mirrors Python's POST /api/v1/ipam/dhcp/
// scopes/{id}/reconcile/sync (api/ipam.py:2308). Walks the same
// taxonomy as GET /reconcile but materializes the unbacked and
// dhcp-source entries: INSERT a source=reservation row for unbacked,
// flip source=dhcp → reservation for promoted. static-source rows
// and existing reservations are skipped (operator-owned / already-
// correct).
//
// Distinct capability: ipam:dhcp-scopes:reconcile-sync — operators
// who can see the cross-check (PR 12) may not be the ones authorized
// to mutate IPAM from it.
func (h *Handler) reconcileSyncDhcpScope(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if !h.enforceDhcpScopeFabric(w, r, id, "ipam:dhcp-scopes:reconcile-sync") {
		return
	}
	scope, err := h.Q.GetDhcpScope(r.Context(), id)
	if err != nil {
		mapErr(w, err, errDhcpScopeNotFoundCRUD)
		return
	}
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
	report, err := reconcile.Sync(r.Context(), h.Q, scope.ID, scope.SubnetID, scope.ReservationsJSON, ipRows)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	// Audit metadata mirrors Python at api/ipam.py:2346-2355 — the
	// per-decision counters but NOT the entries (operators read the
	// response body for that; the audit row keeps a stable shape).
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "dhcp_scope.reconcile_sync",
		TargetType: "dhcp_scope", TargetID: id.String(),
		Metadata: map[string]any{
			"subnet_id":             report.SubnetID,
			"upserted":              report.Upserted,
			"promoted":              report.Promoted,
			"skipped_collision":     report.SkippedCollision,
			"skipped_clean":         report.SkippedClean,
			"skipped_mac_mismatch":  report.SkippedMacMismatch,
			"skipped_duid_mismatch": report.SkippedDuidMismatch,
			"skipped_no_subnet":     report.SkippedNoSubnet,
		},
	})
	httpx.JSON(w, http.StatusOK, report)
}
