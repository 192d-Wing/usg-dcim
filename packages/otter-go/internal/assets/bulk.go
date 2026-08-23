// Bulk asset upsert (PR 69). Ports POST /assets/bulk from
// api/inventory.py:755. Upserts keyed on (manufacturer, serial) —
// rows with both fields find an existing asset and replace its
// mutable fields; rows missing either field always insert.
//
// Per-row ABAC: site-scope enforcement runs per row (matches the
// pattern established for IPAM bulk in PR 67/68). A row whose
// site_id is outside the caller's scope fails just that row, not
// the whole batch. The Python bulk handler omits this check; the
// Go port is stricter for parity with the rest of the codebase.
package assets

import (
	"context"
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

// bulkResult mirrors schemas.common.BulkResult so the wire shape is
// identical across backends. `errors[]` is always a non-nil slice
// so the JSON serialization is `[]` not `null` on success.
type bulkResult struct {
	Inserted int              `json:"inserted"`
	Updated  int              `json:"updated"`
	Skipped  int              `json:"skipped"`
	Failed   int              `json:"failed"`
	Errors   []map[string]any `json:"errors"`
}

func (h *Handler) bulkUpsert(w http.ResponseWriter, r *http.Request) {
	var rows []createReq
	if err := json.NewDecoder(r.Body).Decode(&rows); err != nil {
		httpx.Error(w, http.StatusBadRequest, "payload must be a JSON array")
		return
	}
	out := bulkResult{Errors: []map[string]any{}}
	for i, row := range rows {
		h.bulkUpsertRow(r.Context(), i, row, &out)
	}
	// One audit row per batch — matches Python (single record after
	// the loop with aggregate counts in metadata).
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action:     "asset.bulk_upsert",
		TargetType: "asset",
	})
	httpx.JSON(w, http.StatusOK, out)
}

// bulkUpsertRow runs the per-row find-or-insert. Split from the
// loop body so the row-error path is one place.
func (h *Handler) bulkUpsertRow(
	ctx context.Context, i int, row createReq, out *bulkResult,
) {
	addErr := func(msg string) {
		out.Failed++
		serial := ""
		if row.Serial != nil {
			serial = *row.Serial
		}
		out.Errors = append(out.Errors, map[string]any{
			"row":    i,
			"serial": serial,
			"error":  msg,
		})
	}
	if row.SiteID == uuid.Nil || row.Name == "" || row.Kind == "" {
		addErr("site_id, name, kind required")
		return
	}
	// Per-row site-scope check (PR 67/68 pattern). The Python
	// handler omits this; we do it here for consistency with the
	// rest of the bulk surface.
	p, _ := auth.From(ctx)
	if err := auth.EnforceSiteScope(ctx, h.Q, p, row.SiteID, "inventory:bulk:execute"); err != nil {
		addErr(err.Error())
		return
	}

	// Upsert key: both manufacturer and serial must be set, else
	// we always insert (matches Python's existing=None branch).
	var existing *dbq.Asset
	if row.Manufacturer != nil && *row.Manufacturer != "" &&
		row.Serial != nil && *row.Serial != "" {
		a, err := h.Q.FindAssetByManufacturerSerial(ctx, dbq.FindAssetByManufacturerSerialParams{Manufacturer: *row.Manufacturer, Serial: *row.Serial})
		if err == nil {
			existing = &a
		} else if !errors.Is(err, pgx.ErrNoRows) {
			addErr(err.Error())
			return
		}
	}

	if existing == nil {
		h.bulkInsert(ctx, row, out, addErr)
		return
	}
	h.bulkUpdate(ctx, *existing, row, out, addErr)
}

func (h *Handler) bulkInsert(
	ctx context.Context, row createReq, out *bulkResult, addErr func(string),
) {
	face := row.Face
	if face == "" {
		face = "front"
	}
	mount := row.Mount
	if mount == "" {
		mount = "rack"
	}
	lifecycle := row.LifecycleState
	if lifecycle == "" {
		lifecycle = "active"
	}
	_, err := h.Q.CreateAsset(ctx, dbq.CreateAssetParams{
		SiteID: row.SiteID, RackID: row.RackID, ParentAssetID: row.ParentAssetID,
		Name: row.Name, Hostname: row.Hostname, Kind: row.Kind,
		Manufacturer: row.Manufacturer, Model: row.Model, Serial: row.Serial,
		Firmware: row.Firmware, RackPositionU: row.RackPositionU, RackUnits: row.RackUnits,
		Face: face, Mount: mount, PduSide: row.PduSide,
		PsuCount: row.PsuCount, PortCount: row.PortCount,
		MgmtIP: row.MgmtIP, MgmtProtocol: row.MgmtProtocol, MgmtPort: row.MgmtPort,
		MgmtCredentialsRef: row.MgmtCredentialsRef,
		LifecycleState:     lifecycle, MetadataJson: row.MetadataJson,
	})
	if err != nil {
		addErr(err.Error())
		return
	}
	out.Inserted++
}

func (h *Handler) bulkUpdate(
	ctx context.Context, existing dbq.Asset, row createReq,
	out *bulkResult, addErr func(string),
) {
	// Python's upsert calls setattr on every field including
	// site_id / kind / manufacturer / serial. UpdateAsset
	// deliberately treats those as immutable — they're either the
	// upsert key (manufacturer, serial) or the placement contract
	// (site_id, kind). Operators who need to relocate or retype an
	// asset use the single-row PATCH for cross-site moves or
	// migrate via a delete + insert.
	face := row.Face
	if face == "" {
		face = "front"
	}
	mount := row.Mount
	if mount == "" {
		mount = "rack"
	}
	lifecycle := row.LifecycleState
	if lifecycle == "" {
		lifecycle = existing.LifecycleState
	}
	_, err := h.Q.UpdateAsset(ctx, dbq.UpdateAssetParams{
		ID:               existing.ID,
		Name:             &row.Name,
		HostnameSet:      true, Hostname: row.Hostname,
		RackIDSet:        true, RackID: row.RackID,
		RackPositionUSet: true, RackPositionU: row.RackPositionU,
		RackUnitsSet:     true, RackUnits: row.RackUnits,
		Face: &face, Mount: &mount,
		PduSideSet:      true, PduSide: row.PduSide,
		PsuCountSet:     true, PsuCount: row.PsuCount,
		PortCountSet:    true, PortCount: row.PortCount,
		MgmtIPSet:       true, MgmtIP: row.MgmtIP,
		MgmtProtocolSet: true, MgmtProtocol: row.MgmtProtocol,
		MgmtPortSet:     true, MgmtPort: row.MgmtPort,
		FirmwareSet:     true, Firmware: row.Firmware,
		LifecycleState:  &lifecycle,
		MetadataJson:    row.MetadataJson,
	})
	if err != nil {
		addErr(err.Error())
		return
	}
	out.Updated++
}
