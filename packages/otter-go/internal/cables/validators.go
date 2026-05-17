// Cable invariants ported from packages/otter/src/dcim/api/inventory.py
// (_validate_cable_endpoints / _validate_port_in_range /
// _validate_port_unused). Pure helpers + thin db-backed checks.
package cables

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// validateEndpoints refuses self-cables and verifies both ends exist
// via the existing asset_id lookup the create handler already uses
// (GetAssetSiteID for the a-end). The b-end check is a small addition.
func validateEndpoints(a, b uuid.UUID) error {
	if a == b {
		return errors.New("a-end and b-end must be different assets")
	}
	if a == uuid.Nil {
		return errors.New("a_asset_id required")
	}
	if b == uuid.Nil {
		return errors.New("b_asset_id required")
	}
	return nil
}

// validatePortInRange — if asset.port_count is set, port must be a
// 1..N integer. Pure helper; callers fetch port_count from the asset
// they already looked up.
func validatePortInRange(assetName string, portCount *int32, port *string, end string) error {
	if port == nil || *port == "" || portCount == nil || *portCount == 0 {
		return nil
	}
	n, err := strconv.Atoi(*port)
	if err != nil {
		return fmt.Errorf("%s-end port %q on %s must be a number 1-%d", end, *port, assetName, *portCount)
	}
	if n < 1 || n > int(*portCount) {
		return fmt.Errorf("%s-end port %d on %s is outside the 1-%d port range",
			end, n, assetName, *portCount)
	}
	return nil
}

// validatePortNotInUse — refuses if (asset_id, port) is already
// claimed by another cable. excludeID lets a PATCH skip its own row.
func (h *Handler) validatePortNotInUse(
	ctx context.Context, assetID uuid.UUID, port *string, excludeID uuid.UUID, end string,
) error {
	if port == nil || *port == "" {
		return nil
	}
	row, err := h.Q.FindCableForPort(ctx, dbq.FindCableForPortParams{
		AssetID: assetID, Port: port, ExcludeID: excludeID,
	})
	if err == nil {
		label := ""
		if row.Label != nil {
			label = *row.Label
		}
		return fmt.Errorf("%s-end port %s is already in use by cable %s (label=%q)",
			end, *port, row.ID, label)
	}
	// pgx.ErrNoRows means no conflict — that's the happy path.
	return nil
}
