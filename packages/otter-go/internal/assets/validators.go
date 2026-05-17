// Asset placement validation ported from
// packages/otter/src/dcim/api/inventory.py:_check_u_grid_fit and
// _validate_placement_and_resolve_target.
//
// Refuses asset POST/PATCH whose (rack_id, rack_position_u, rack_units,
// face) would:
//   * overflow the rack's u_height envelope
//   * collide with another rack-mounted asset on the same face
package assets

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// validateUGridFit walks the rack's placed assets on the given face
// (minus excludeID for PATCH) and refuses overflow or overlap. assetID
// uuid.Nil signals a create (no row to exclude).
func (h *Handler) validateUGridFit(
	ctx context.Context,
	rackID uuid.UUID,
	excludeID uuid.UUID,
	positionU int32,
	units int32,
	face string,
) error {
	if units < 1 {
		units = 1
	}
	rack, err := h.Q.GetRack(ctx, rackID)
	if err != nil {
		return fmt.Errorf("target rack %s not found", rackID)
	}
	top := positionU + units - 1
	if positionU < 1 || top > rack.UHeight {
		return fmt.Errorf("placement U%d-U%d overflows %dU rack", positionU, top, rack.UHeight)
	}
	others, err := h.Q.ListRackAssetsForPlacement(ctx, dbq.ListRackAssetsForPlacementParams{
		RackID: rackID, ExcludeID: excludeID, Face: face,
	})
	if err != nil {
		return fmt.Errorf("rack placement lookup failed: %w", err)
	}
	var collisions []string
	for _, o := range others {
		if o.RackPositionU == nil {
			continue
		}
		oUnits := int32(1)
		if o.RackUnits != nil && *o.RackUnits > 0 {
			oUnits = *o.RackUnits
		}
		oTop := *o.RackPositionU + oUnits - 1
		// Overlap iff requested top >= other start AND requested start <= other top.
		if top >= *o.RackPositionU && positionU <= oTop {
			collisions = append(collisions, fmt.Sprintf("%s (U%d, size=%d)", o.Name, *o.RackPositionU, oUnits))
		}
	}
	if len(collisions) > 0 {
		return fmt.Errorf("placement U%d-U%d collides on %s face with: %v", positionU, top, face, collisions)
	}
	return nil
}

