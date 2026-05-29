// POST /api/v1/ipam/supernets/{id}/move — relocates a tenant-owned
// supernet from the LIR landing fabric to its operational fabric/VRF.
//
// This is the bridge endpoint that completes the LIR allocation
// flow: phase 4 (approval) lands the new tenant Supernet in the
// system 'lir-unassigned' fabric; phase 7 (this file) lets the
// tenant — once they've decided how they want to use the space —
// pick the destination fabric + VRF and move it.
//
// Guards (in order):
//   1. Supernet exists.
//   2. Currently in the landing fabric (slug = landingFabricSlug).
//   3. Tenant-owned (owner_organization_id IS NOT NULL).
//   4. Principal's ipam:supernets:update scope covers the owner org.
//   5. Body's target_fabric_id and target_vrf_id parse as UUIDs.
//   6. Target VRF exists and belongs to the target fabric.
//   7. Principal's ipam:supernets:update fabric-scope covers the
//      target fabric.
//   8. No child subnets exist (enforced by NOT EXISTS in the
//      atomic UPDATE so a racer that inserts a subnet between our
//      pre-check and the move can't sneak through).
package ipam

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

const capSupernetsUpdate = "ipam:supernets:update"

type moveSupernetReq struct {
	FabricID string `json:"fabric_id"`
	VrfID    string `json:"vrf_id"`
}

func (h *Handler) moveSupernet(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	p, _ := auth.From(r.Context())

	src, ok := h.loadMoveable(r, w, id, p)
	if !ok {
		return
	}

	target, ok := h.parseAndValidateMoveTarget(r, w, p)
	if !ok {
		return
	}

	out, err := h.Q.MoveSupernet(r.Context(), dbq.MoveSupernetParams{
		ID:                      id,
		TargetFabricID:          target.fabricID,
		TargetVrfID:             target.vrfID,
		ExpectedCurrentFabricID: src.CurrentFabricID,
	})
	if err != nil {
		// CTE's WHERE matched none of the rows — either a racer
		// already moved it out of the landing fabric, or a child
		// subnet got inserted between our checks and the UPDATE.
		// 409 with a re-fetch hint matches the LIR module's race
		// posture.
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusConflict,
				"supernet raced: it left the landing fabric or gained a child subnet; re-fetch and retry")
			return
		}
		mapErr(w, err, "supernet not found")
		return
	}

	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "ipam.supernet.move", TargetType: "supernet",
		TargetID: id.String(),
		Metadata: map[string]any{
			"prefix":             src.Prefix,
			"source_fabric_id":   src.CurrentFabricID.String(),
			"source_vrf_id":      src.CurrentVrfID.String(),
			"target_fabric_id":   target.fabricID.String(),
			"target_vrf_id":      target.vrfID.String(),
			"owner_organization": ownerOrgString(src.OwnerOrganizationID),
		},
	})
	httpx.JSON(w, http.StatusOK, out)
}

// loadMoveable pre-fetches the supernet and runs all source-side
// guards: existence, landing-fabric check, tenant-owned check,
// org-scope check. Returns the row + ok=true on success, or writes
// the appropriate error and returns ok=false.
func (h *Handler) loadMoveable(
	r *http.Request, w http.ResponseWriter, id uuid.UUID, p auth.Principal,
) (dbq.SupernetForMoveRow, bool) {
	src, err := h.Q.GetSupernetForMove(r.Context(), id)
	if err != nil {
		mapErr(w, err, "supernet not found")
		return dbq.SupernetForMoveRow{}, false
	}
	if !src.CurrentFabricIsSystem {
		// is_system is set only on platform-managed fabrics (today
		// just the LIR landing fabric seeded by migration 0065). The
		// guard used to compare against a hardcoded slug literal
		// duplicated from the lir package; switching to the column
		// keeps the contract on the row, not on a string. If a
		// future deployment adds another is_system fabric, this
		// check needs a sub-type column to disambiguate.
		httpx.Error(w, http.StatusConflict,
			"supernet is not in the LIR landing fabric; nothing to move")
		return dbq.SupernetForMoveRow{}, false
	}
	if src.OwnerOrganizationID == nil {
		httpx.Error(w, http.StatusConflict,
			"only tenant-owned supernets (with owner_organization_id) can be moved")
		return dbq.SupernetForMoveRow{}, false
	}
	if scope := auth.FindScope(p, capSupernetsUpdate); scope != nil &&
		!scope.OrganizationMatches(*src.OwnerOrganizationID) {
		// 403 mirrors the IPAM module's existing fabric-scope refusal
		// shape — the operator explicitly targeted this UUID, so a
		// pure existence-leak hide isn't worth the inconsistency
		// against the rest of /ipam/*.
		httpx.Error(w, http.StatusForbidden,
			"supernet's owner organization is outside your scope for ipam:supernets:update")
		return dbq.SupernetForMoveRow{}, false
	}
	return src, true
}

type moveTarget struct {
	fabricID uuid.UUID
	vrfID    uuid.UUID
}

// parseAndValidateMoveTarget decodes the body, parses the UUIDs,
// looks up the target VRF, confirms the VRF lives in the target
// fabric, and runs the fabric-scope check. Returns the validated
// target + ok=true on success.
func (h *Handler) parseAndValidateMoveTarget(
	r *http.Request, w http.ResponseWriter, p auth.Principal,
) (moveTarget, bool) {
	var req moveSupernetReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return moveTarget{}, false
	}
	if req.FabricID == "" || req.VrfID == "" {
		httpx.Error(w, http.StatusBadRequest,
			"fabric_id and vrf_id are required")
		return moveTarget{}, false
	}
	fabricID, err := uuid.Parse(req.FabricID)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "fabric_id is not a uuid")
		return moveTarget{}, false
	}
	vrfID, err := uuid.Parse(req.VrfID)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "vrf_id is not a uuid")
		return moveTarget{}, false
	}
	vrf, err := h.Q.GetVrfForMove(r.Context(), vrfID)
	if err != nil {
		mapErr(w, err, "target vrf not found")
		return moveTarget{}, false
	}
	if vrf.FabricID != fabricID {
		httpx.Error(w, http.StatusUnprocessableEntity,
			"target vrf does not belong to target fabric")
		return moveTarget{}, false
	}
	if err := auth.EnforceFabricScope(p, fabricID, capSupernetsUpdate); err != nil {
		httpx.Error(w, http.StatusForbidden, err.Error())
		return moveTarget{}, false
	}
	return moveTarget{fabricID: fabricID, vrfID: vrfID}, true
}

// ownerOrgString — JSON-safe formatter for an optional UUID.
// loadMoveable already rejects nil-owner supernets, so by the time
// this is called src.OwnerOrganizationID is always non-nil. The nil
// branch is a defensive belt-and-suspenders since the audit metadata
// shape must always render.
func ownerOrgString(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}
