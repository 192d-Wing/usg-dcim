package power

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type connectReq struct {
	AssetID     uuid.UUID `json:"asset_id"`
	PsuIndex    *int32    `json:"psu_index"`
	CordColor   *string   `json:"cord_color"`
	CordLengthM *float64  `json:"cord_length_m"`
}

// connectOut is the connect response shape. Matches Python's
// PowerConnectionOut: id, outlet_id, asset_id, psu_index, cord_color,
// cord_length_m, created_at — no updated_at.
type connectOut struct {
	ID          uuid.UUID `json:"id"`
	OutletID    uuid.UUID `json:"outlet_id"`
	AssetID     uuid.UUID `json:"asset_id"`
	PsuIndex    int32     `json:"psu_index"`
	CordColor   *string   `json:"cord_color"`
	CordLengthM *string   `json:"cord_length_m"`
	CreatedAt   time.Time `json:"created_at"`
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

	if _, err := h.Q.GetOutletByID(r.Context(), outletID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "outlet not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	asset, err := h.Q.GetAsset(r.Context(), req.AssetID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "asset not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}

	// outlet may only carry one connection — friendly message
	// matches Python's ConflictError text. The UNIQUE constraint
	// would surface a generic 409 otherwise.
	if _, err := h.Q.GetPowerConnectionByOutlet(r.Context(), outletID); err == nil {
		httpx.Error(w, http.StatusConflict, "outlet is already connected; disconnect it first")
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
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
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	siteID := asset.SiteID
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action:     "power.connect",
		TargetType: "outlet",
		TargetID:   outletID.String(),
		SiteID:     &siteID,
		Metadata:   map[string]any{"asset_id": req.AssetID.String(), "psu_index": psuIndex},
	})
	httpx.JSON(w, http.StatusCreated, connectOut{
		ID: out.ID, OutletID: out.OutletID, AssetID: out.AssetID,
		PsuIndex: out.PsuIndex, CordColor: out.CordColor, CordLengthM: out.CordLengthM,
		CreatedAt: out.CreatedAt,
	})
}

func (h *Handler) disconnect(w http.ResponseWriter, r *http.Request) {
	outletID, err := uuid.Parse(chi.URLParam(r, "outlet_id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "outlet_id is not a uuid")
		return
	}
	conn, err := h.Q.GetPowerConnectionByOutlet(r.Context(), outletID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "no connection on this outlet")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	// Mirror Python: db.get(Asset, id) returns None for not-found and
	// raises on transient errors. ErrNoRows → site_id stays nil in the
	// audit row; any other error short-circuits before DELETE so we
	// don't end up disconnecting with a degraded audit trail.
	asset, assetErr := h.Q.GetAsset(r.Context(), conn.AssetID)
	if assetErr != nil && !errors.Is(assetErr, pgx.ErrNoRows) {
		status, msg := httpx.Mapped(assetErr)
		httpx.Error(w, status, msg)
		return
	}
	if err := h.Q.DeleteOutletConnection(r.Context(), outletID); err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	ev := audit.Event{
		Action:     "power.disconnect",
		TargetType: "outlet",
		TargetID:   outletID.String(),
		Metadata:   map[string]any{"asset_id": conn.AssetID.String()},
	}
	if assetErr == nil {
		siteID := asset.SiteID
		ev.SiteID = &siteID
	}
	audit.Record(r.Context(), h.Audit, nil, ev)
	w.WriteHeader(http.StatusNoContent)
}
