package assets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// enforceSite is a tiny wrapper so the asset mutation handlers can stay
// terse. Returns false (after writing 403) when the caller's scope
// rejects siteID for capCode.
func (h *Handler) enforceSite(w http.ResponseWriter, r *http.Request, siteID uuid.UUID, capCode string) bool {
	p, _ := auth.From(r.Context())
	if err := auth.EnforceSiteScope(r.Context(), h.Q, p, siteID, capCode); err != nil {
		httpx.Error(w, http.StatusForbidden, err.Error())
		return false
	}
	return true
}

const msgAssetNotFound = "asset not found"

// loadAssetForMutation is the shared prefetch chain of update,
// decommission (and its preview), and delete: fetch the asset (404
// when it doesn't exist) and enforce the caller's site scope for
// capCode (403). ok=false means the response has already been written.
func (h *Handler) loadAssetForMutation(w http.ResponseWriter, r *http.Request, id uuid.UUID, capCode string) (dbq.Asset, bool) {
	current, err := h.Q.GetAsset(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, msgAssetNotFound)
			return dbq.Asset{}, false
		}
		httpx.WriteMapped(w, err)
		return dbq.Asset{}, false
	}
	if !h.enforceSite(w, r, current.SiteID, capCode) {
		return dbq.Asset{}, false
	}
	return current, true
}

// NOTE: PR 41 ports the basic CRUD + decommission. The following are
// intentionally deferred and will arrive in a focused follow-up PR:
//   - PDU outlet auto-seeding on POST /assets (Python create_asset
//     seeds 24 outlets for new PDUs; needs a transaction wrapper)
//   - u-grid placement validation on POST/PATCH (Python's
//     _check_u_grid_fit refuses overflow + collision)
//   - cross-site move site_id sync on PATCH (target rack's site)

type createReq struct {
	SiteID             uuid.UUID  `json:"site_id"`
	RackID             *uuid.UUID `json:"rack_id"`
	ParentAssetID      *uuid.UUID `json:"parent_asset_id"`
	Name               string     `json:"name"`
	Hostname           *string    `json:"hostname"`
	Kind               string     `json:"kind"`
	Manufacturer       *string    `json:"manufacturer"`
	Model              *string    `json:"model"`
	Serial             *string    `json:"serial"`
	Firmware           *string    `json:"firmware"`
	RackPositionU      *int32     `json:"rack_position_u"`
	RackUnits          *int32     `json:"rack_units"`
	Face               string     `json:"face"`
	Mount              string     `json:"mount"`
	PduSide            *string    `json:"pdu_side"`
	PsuCount           *int32     `json:"psu_count"`
	PortCount          *int32     `json:"port_count"`
	MgmtIP             *string    `json:"mgmt_ip"`
	MgmtProtocol       *string    `json:"mgmt_protocol"`
	MgmtPort           *int32     `json:"mgmt_port"`
	MgmtCredentialsRef *string    `json:"mgmt_credentials_ref"`
	LifecycleState     string     `json:"lifecycle_state"`
	MetadataJson       json.RawMessage `json:"metadata_json"`
}

// pduOutletCount matches Python create_asset's fixed strip size: new
// PDUs come with a 24-outlet strip (odd positions phase A, even B,
// C13 receptacles — see SeedPduOutlets).
const pduOutletCount = 24

// createAsset runs the asset INSERT — and, for PDUs, the outlet
// auto-seed in the same transaction so a half-created PDU can't
// escape. With no Pool (tests) the PDU path degrades to autocommit:
// asset first, then outlets.
func (h *Handler) createAsset(ctx context.Context, arg dbq.CreateAssetParams) (dbq.Asset, error) {
	if arg.Kind != "pdu" {
		return h.Q.CreateAsset(ctx, arg)
	}
	if h.Pool == nil {
		out, err := h.Q.CreateAsset(ctx, arg)
		if err != nil {
			return out, err
		}
		_, err = h.Q.SeedPduOutlets(ctx, dbq.SeedPduOutletsParams{
			PduAssetID: out.ID, OutletCount: pduOutletCount,
		})
		return out, err
	}
	tx, err := h.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return dbq.Asset{}, err
	}
	// Rollback on a fresh background context so cleanup still runs
	// when the request context is cancelled mid-flight (same pattern
	// as bgp's rotate-batch).
	defer func() {
		rbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rbCtx)
	}()
	q := dbq.New(tx)
	out, err := q.CreateAsset(ctx, arg)
	if err != nil {
		return dbq.Asset{}, err
	}
	if _, err := q.SeedPduOutlets(ctx, dbq.SeedPduOutletsParams{
		PduAssetID: out.ID, OutletCount: pduOutletCount,
	}); err != nil {
		return dbq.Asset{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return dbq.Asset{}, err
	}
	return out, nil
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.Name == "" || req.Kind == "" || req.SiteID == uuid.Nil {
		httpx.Error(w, http.StatusBadRequest, "site_id, name, kind required")
		return
	}
	if !h.enforceSite(w, r, req.SiteID, "inventory:assets:create") {
		return
	}
	if req.Face == "" {
		req.Face = "front"
	}
	if req.Mount == "" {
		req.Mount = "rack"
	}
	if req.LifecycleState == "" {
		req.LifecycleState = "active"
	}
	// Placement validation: only when rack-mounted with a position set.
	if req.RackID != nil && req.Mount == "rack" && req.RackPositionU != nil {
		units := int32(1)
		if req.RackUnits != nil && *req.RackUnits > 0 {
			units = *req.RackUnits
		}
		if perr := h.validateUGridFit(r.Context(), *req.RackID, uuid.Nil, *req.RackPositionU, units, req.Face); perr != nil {
			httpx.Error(w, http.StatusConflict, perr.Error())
			return
		}
	}
	out, err := h.createAsset(r.Context(), dbq.CreateAssetParams{
		SiteID: req.SiteID, RackID: req.RackID, ParentAssetID: req.ParentAssetID,
		Name: req.Name, Hostname: req.Hostname, Kind: req.Kind,
		Manufacturer: req.Manufacturer, Model: req.Model, Serial: req.Serial,
		Firmware: req.Firmware, RackPositionU: req.RackPositionU, RackUnits: req.RackUnits,
		Face: req.Face, Mount: req.Mount, PduSide: req.PduSide,
		PsuCount: req.PsuCount, PortCount: req.PortCount,
		MgmtIP: req.MgmtIP, MgmtProtocol: req.MgmtProtocol, MgmtPort: req.MgmtPort,
		MgmtCredentialsRef: req.MgmtCredentialsRef,
		LifecycleState: req.LifecycleState, MetadataJson: req.MetadataJson,
	})
	if err != nil {
		httpx.WriteMapped(w, err)
		return
	}
	sid := out.SiteID
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "asset.create", TargetType: "asset", TargetID: out.ID.String(), SiteID: &sid,
	})
	httpx.JSON(w, http.StatusCreated, out)
}

type updateReq struct {
	Name              *string
	Hostname          *string
	hostnameSet       bool
	RackID            *uuid.UUID
	rackIDSet         bool
	RackPositionU     *int32
	rackPositionUSet  bool
	RackUnits         *int32
	rackUnitsSet      bool
	Face              *string
	Mount             *string
	PduSide           *string
	pduSideSet        bool
	PsuCount          *int32
	psuCountSet       bool
	PortCount         *int32
	portCountSet      bool
	MgmtIP            *string
	mgmtIPSet         bool
	MgmtProtocol      *string
	mgmtProtocolSet   bool
	MgmtPort          *int32
	mgmtPortSet       bool
	Firmware          *string
	firmwareSet       bool
	LifecycleState    *string
	MetadataJson      json.RawMessage
}

func (u *updateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	type setter struct {
		key string
		set *bool
		dst any
	}
	setters := []setter{
		{"hostname", &u.hostnameSet, &u.Hostname},
		{"rack_id", &u.rackIDSet, &u.RackID},
		{"rack_position_u", &u.rackPositionUSet, &u.RackPositionU},
		{"rack_units", &u.rackUnitsSet, &u.RackUnits},
		{"pdu_side", &u.pduSideSet, &u.PduSide},
		{"psu_count", &u.psuCountSet, &u.PsuCount},
		{"port_count", &u.portCountSet, &u.PortCount},
		{"mgmt_ip", &u.mgmtIPSet, &u.MgmtIP},
		{"mgmt_protocol", &u.mgmtProtocolSet, &u.MgmtProtocol},
		{"mgmt_port", &u.mgmtPortSet, &u.MgmtPort},
		{"firmware", &u.firmwareSet, &u.Firmware},
	}
	for _, s := range setters {
		if v, ok := raw[s.key]; ok {
			*s.set = true
			if err := json.Unmarshal(v, s.dst); err != nil {
				return err
			}
		}
	}
	// Non-tracked (always-replace) fields
	if v, ok := raw["name"]; ok {
		if err := json.Unmarshal(v, &u.Name); err != nil {
			return err
		}
	}
	if v, ok := raw["face"]; ok {
		if err := json.Unmarshal(v, &u.Face); err != nil {
			return err
		}
	}
	if v, ok := raw["mount"]; ok {
		if err := json.Unmarshal(v, &u.Mount); err != nil {
			return err
		}
	}
	if v, ok := raw["lifecycle_state"]; ok {
		if err := json.Unmarshal(v, &u.LifecycleState); err != nil {
			return err
		}
	}
	if v, ok := raw["metadata_json"]; ok {
		u.MetadataJson = v
	}
	return nil
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.IDParam(w, r)
	if !ok {
		return
	}
	var req updateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	// PR 54 ABAC: resolve current site, enforce before write.
	if _, ok := h.loadAssetForMutation(w, r, id, "inventory:assets:update"); !ok {
		return
	}
	out, err := h.Q.UpdateAsset(r.Context(), dbq.UpdateAssetParams{
		ID: id, Name: req.Name,
		HostnameSet: req.hostnameSet, Hostname: req.Hostname,
		RackIDSet: req.rackIDSet, RackID: req.RackID,
		RackPositionUSet: req.rackPositionUSet, RackPositionU: req.RackPositionU,
		RackUnitsSet: req.rackUnitsSet, RackUnits: req.RackUnits,
		Face: req.Face, Mount: req.Mount,
		PduSideSet: req.pduSideSet, PduSide: req.PduSide,
		PsuCountSet: req.psuCountSet, PsuCount: req.PsuCount,
		PortCountSet: req.portCountSet, PortCount: req.PortCount,
		MgmtIPSet: req.mgmtIPSet, MgmtIP: req.MgmtIP,
		MgmtProtocolSet: req.mgmtProtocolSet, MgmtProtocol: req.MgmtProtocol,
		MgmtPortSet: req.mgmtPortSet, MgmtPort: req.MgmtPort,
		FirmwareSet: req.firmwareSet, Firmware: req.Firmware,
		LifecycleState: req.LifecycleState, MetadataJson: req.MetadataJson,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, msgAssetNotFound)
			return
		}
		httpx.WriteMapped(w, err)
		return
	}
	sid := out.SiteID
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "asset.update", TargetType: "asset", TargetID: id.String(), SiteID: &sid,
	})
	httpx.JSON(w, http.StatusOK, out)
}

// ---- Hard delete ----

// delete hard-removes an asset. Decommission remains the lifecycle
// path (it only flips lifecycle_state); DELETE is for mistakes and
// test hygiene. Child assets and logged cables refuse with 409; IP
// bindings detach and this asset's alerts drop first so the row can
// go. Outlets + power connections ride ON DELETE CASCADE. Telemetry-
// instrumented assets hit the FK RESTRICT, which httpx.Mapped turns
// into a 409 — acceptable: those aren't "mistake" assets.
//
// Sequential autocommit calls, no tx: detach/delete steps are
// idempotent-ish and a failure midway leaves only detached IPs /
// dropped alerts, which a retry (or decommission) cleans up.
func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.IDParam(w, r)
	if !ok {
		return
	}
	current, ok := h.loadAssetForMutation(w, r, id, "inventory:assets:delete")
	if !ok {
		return
	}
	// 409 guards: each counts a dependent that must be gone first.
	guards := []struct {
		count func() (int64, error)
		msg   string
	}{
		{func() (int64, error) { return h.Q.CountChildAssets(r.Context(), &id) },
			"asset has %d child assets; move or delete them first"},
		{func() (int64, error) { return h.Q.CountCablesForAsset(r.Context(), id) },
			"asset has %d cables logged; delete them first"},
	}
	for _, g := range guards {
		n, err := g.count()
		if err != nil {
			httpx.WriteMapped(w, err)
			return
		}
		if n > 0 {
			httpx.Error(w, http.StatusConflict, fmt.Sprintf(g.msg, n))
			return
		}
	}
	detachedIPs, err := h.Q.DetachIPAddressesFromAsset(r.Context(), &id)
	if err != nil {
		httpx.WriteMapped(w, err)
		return
	}
	droppedAlerts, err := h.Q.DeleteAlertsForAsset(r.Context(), &id)
	if err != nil {
		httpx.WriteMapped(w, err)
		return
	}
	rows, err := h.Q.DeleteAsset(r.Context(), id)
	if err != nil {
		httpx.WriteMapped(w, err)
		return
	}
	if rows == 0 {
		// Raced with a concurrent delete between the pre-fetch and here.
		httpx.Error(w, http.StatusNotFound, msgAssetNotFound)
		return
	}
	sid := current.SiteID
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "asset.delete", TargetType: "asset", TargetID: id.String(), SiteID: &sid,
		Metadata: map[string]any{
			"detached_ip_addresses": detachedIPs,
			"deleted_alerts":        droppedAlerts,
		},
	})
	w.WriteHeader(http.StatusNoContent)
}

// ---- Decommission ----

type decommissionImpact struct {
	ConsumerDrops    int64    `json:"consumer_drops"`
	PduDrops         int64    `json:"pdu_drops"`
	DownstreamAssets []string `json:"downstream_assets"`
}

type decommissionResult struct {
	Asset  dbq.Asset          `json:"asset"`
	Impact decommissionImpact `json:"impact"`
}

func (h *Handler) computeImpact(r *http.Request, assetID uuid.UUID) (decommissionImpact, error) {
	var imp decommissionImpact
	cd, err := h.Q.CountConsumerPowerDrops(r.Context(), assetID)
	if err != nil {
		return imp, err
	}
	imp.ConsumerDrops = cd
	pd, err := h.Q.CountPduPowerDrops(r.Context(), assetID)
	if err != nil {
		return imp, err
	}
	imp.PduDrops = pd
	names, err := h.Q.ListDownstreamAssetNames(r.Context(), assetID)
	if err != nil {
		return imp, err
	}
	imp.DownstreamAssets = names
	if imp.DownstreamAssets == nil {
		imp.DownstreamAssets = []string{}
	}
	return imp, nil
}

func (h *Handler) decommissionPreview(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.IDParam(w, r)
	if !ok {
		return
	}
	// 404 if asset doesn't exist — matches Python's preflight check.
	if _, ok := h.loadAssetForMutation(w, r, id, "inventory:assets:read"); !ok {
		return
	}
	imp, err := h.computeImpact(r, id)
	if err != nil {
		httpx.WriteMapped(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, imp)
}

func (h *Handler) decommission(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.IDParam(w, r)
	if !ok {
		return
	}
	current, ok := h.loadAssetForMutation(w, r, id, "inventory:assets:update")
	if !ok {
		return
	}
	if current.LifecycleState == "decommissioned" {
		httpx.Error(w, http.StatusBadRequest, "asset is already decommissioned")
		return
	}
	// Compute impact BEFORE deletes so the response carries accurate counts.
	imp, err := h.computeImpact(r, id)
	if err != nil {
		httpx.WriteMapped(w, err)
		return
	}
	if err := h.Q.DeleteConsumerPowerConnections(r.Context(), id); err != nil {
		httpx.WriteMapped(w, err)
		return
	}
	if err := h.Q.DeletePduPowerConnections(r.Context(), id); err != nil {
		httpx.WriteMapped(w, err)
		return
	}
	updated, err := h.Q.SetAssetDecommissioned(r.Context(), id)
	if err != nil {
		httpx.WriteMapped(w, err)
		return
	}
	sid := updated.SiteID
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "asset.decommission", TargetType: "asset", TargetID: id.String(), SiteID: &sid,
		Metadata: map[string]any{
			"consumer_drops":    imp.ConsumerDrops,
			"pdu_drops":         imp.PduDrops,
			"downstream_assets": imp.DownstreamAssets,
		},
	})
	httpx.JSON(w, http.StatusOK, decommissionResult{Asset: updated, Impact: imp})
}
