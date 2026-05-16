package assets

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

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

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.Name == "" || req.Kind == "" || req.SiteID == uuid.Nil {
		httpx.Error(w, http.StatusBadRequest, "site_id, name, kind required")
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
	out, err := h.Q.CreateAsset(r.Context(), dbq.CreateAssetParams{
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
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
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
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	var req updateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
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
			httpx.Error(w, http.StatusNotFound, "asset not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
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
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	// 404 if asset doesn't exist — matches Python's preflight check.
	if _, err := h.Q.GetAsset(r.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "asset not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	imp, err := h.computeImpact(r, id)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, imp)
}

func (h *Handler) decommission(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	current, err := h.Q.GetAsset(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "asset not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if current.LifecycleState == "decommissioned" {
		httpx.Error(w, http.StatusBadRequest, "asset is already decommissioned")
		return
	}
	// Compute impact BEFORE deletes so the response carries accurate counts.
	imp, err := h.computeImpact(r, id)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if err := h.Q.DeleteConsumerPowerConnections(r.Context(), id); err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if err := h.Q.DeletePduPowerConnections(r.Context(), id); err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	updated, err := h.Q.SetAssetDecommissioned(r.Context(), id)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, decommissionResult{Asset: updated, Impact: imp})
}
