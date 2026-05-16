package cables

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
	siteID, err := h.Q.GetAssetSiteID(r.Context(), req.AAssetID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusBadRequest, "a_asset_id not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
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
	w.WriteHeader(http.StatusNoContent)
}
