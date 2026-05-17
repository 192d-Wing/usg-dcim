package power

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type connectReq struct {
	AssetID     uuid.UUID `json:"asset_id"`
	PsuIndex    *int32    `json:"psu_index"`
	CordColor   *string   `json:"cord_color"`
	CordLengthM *float64  `json:"cord_length_m"`
}

func (h *Handler) connect(w http.ResponseWriter, r *http.Request) {
	outletID, err := uuid.Parse(chi.URLParam(r, "outlet_id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "outlet_id is not a uuid")
		return
	}
	var req connectReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.AssetID == uuid.Nil {
		httpx.Error(w, http.StatusBadRequest, "asset_id required")
		return
	}
	psuIndex := int32(1)
	if req.PsuIndex != nil {
		psuIndex = *req.PsuIndex
	}
	var cordStr *string
	if req.CordLengthM != nil {
		s := strconv.FormatFloat(*req.CordLengthM, 'f', -1, 64)
		cordStr = &s
	}
	out, err := h.Q.CreatePowerConnection(r.Context(), dbq.CreatePowerConnectionParams{
		OutletID: outletID, AssetID: req.AssetID, PsuIndex: psuIndex,
		CordColor: req.CordColor, CordLengthM: cordStr,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "outlet not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *Handler) disconnect(w http.ResponseWriter, r *http.Request) {
	outletID, err := uuid.Parse(chi.URLParam(r, "outlet_id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "outlet_id is not a uuid")
		return
	}
	if err := h.Q.DeleteOutletConnection(r.Context(), outletID); err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
