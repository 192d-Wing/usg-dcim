package cables

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// NOTE: Port-in-range and port-in-use validation (Python's
// _validate_port_in_range + _validate_port_unused) and PATCH are
// deferred to the same invariants follow-up as asset placement
// validation. POST/DELETE here just write through; site_id is set
// from the a-end asset to match Python's behavior.

type createReq struct {
	AAssetID uuid.UUID `json:"a_asset_id"`
	APort    *string   `json:"a_port"`
	BAssetID uuid.UUID `json:"b_asset_id"`
	BPort    *string   `json:"b_port"`
	Medium   *string   `json:"medium"`
	Color    *string   `json:"color"`
	LengthM  *string   `json:"length_m"`
	Label    *string   `json:"label"`
	Face     *string   `json:"face"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.AAssetID == uuid.Nil || req.BAssetID == uuid.Nil {
		httpx.Error(w, http.StatusBadRequest, "a_asset_id and b_asset_id required")
		return
	}
	if err := validateEndpoints(req.AAssetID, req.BAssetID); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	aAsset, err := h.Q.GetAsset(r.Context(), req.AAssetID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusBadRequest, "a_asset_id not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	bAsset, err := h.Q.GetAsset(r.Context(), req.BAssetID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusBadRequest, "b_asset_id not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if err := validatePortInRange(aAsset.Name, aAsset.PortCount, req.APort, "a"); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validatePortInRange(bAsset.Name, bAsset.PortCount, req.BPort, "b"); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.validatePortNotInUse(r.Context(), req.AAssetID, req.APort, uuid.Nil, "a"); err != nil {
		httpx.Error(w, http.StatusConflict, err.Error())
		return
	}
	if err := h.validatePortNotInUse(r.Context(), req.BAssetID, req.BPort, uuid.Nil, "b"); err != nil {
		httpx.Error(w, http.StatusConflict, err.Error())
		return
	}
	siteID := aAsset.SiteID
	out, err := h.Q.CreateCable(r.Context(), dbq.CreateCableParams{
		SiteID: siteID, AAssetID: req.AAssetID, APort: req.APort,
		BAssetID: req.BAssetID, BPort: req.BPort,
		Medium: req.Medium, Color: req.Color, LengthM: req.LengthM,
		Label: req.Label, Face: req.Face,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	sid := out.SiteID
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "cable.create", TargetType: "cable", TargetID: out.ID.String(), SiteID: &sid,
		Metadata: map[string]any{
			"a_asset_id": out.AAssetID.String(),
			"b_asset_id": out.BAssetID.String(),
		},
	})
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	if _, err := h.Q.GetCable(r.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "cable not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if err := h.Q.DeleteCable(r.Context(), id); err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "cable.delete", TargetType: "cable", TargetID: id.String(),
	})
	w.WriteHeader(http.StatusNoContent)
}
