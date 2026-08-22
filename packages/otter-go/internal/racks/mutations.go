package racks

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type createReq struct {
	SiteID       uuid.UUID `json:"site_id"`
	RowID        uuid.UUID `json:"row_id"`
	Name         string    `json:"name"`
	Code         string    `json:"code"`
	UHeight      *int32    `json:"u_height"`
	MaxKw        *string   `json:"max_kw"`
	MaxWeightLbs *int32    `json:"max_weight_lbs"`
	Serial       *string   `json:"serial"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.Name == "" || req.Code == "" || req.SiteID == uuid.Nil || req.RowID == uuid.Nil {
		httpx.Error(w, http.StatusBadRequest, "site_id, row_id, name, code required")
		return
	}
	p, _ := auth.From(r.Context())
	if serr := auth.EnforceSiteScope(r.Context(), h.Q, p, req.SiteID, "inventory:racks:create"); serr != nil {
		httpx.Error(w, http.StatusForbidden, serr.Error())
		return
	}
	u := int32(42)
	if req.UHeight != nil {
		u = *req.UHeight
	}
	out, err := h.Q.CreateRack(r.Context(), dbq.CreateRackParams{
		SiteID: req.SiteID, RowID: req.RowID, Name: req.Name, Code: req.Code,
		UHeight: u, MaxKw: req.MaxKw, MaxWeightLbs: req.MaxWeightLbs, Serial: req.Serial,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	sid := out.SiteID
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "rack.create", TargetType: "rack", TargetID: out.ID.String(), SiteID: &sid,
	})
	httpx.JSON(w, http.StatusCreated, out)
}

type updateReq struct {
	Name      *string
	UHeight   *int32
	MaxKw     *string
	maxKwSet  bool
	Serial    *string
	serialSet bool
	// Floor-plan placement. Set flags distinguish "clear placement"
	// (explicit null) from "leave alone" (absent).
	GridX        *int32
	gridXSet     bool
	GridY        *int32
	gridYSet     bool
	GridRotation *int16
}

func (u *updateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["name"]; ok {
		if err := json.Unmarshal(v, &u.Name); err != nil {
			return err
		}
	}
	if v, ok := raw["u_height"]; ok {
		if err := json.Unmarshal(v, &u.UHeight); err != nil {
			return err
		}
	}
	if v, ok := raw["max_kw"]; ok {
		u.maxKwSet = true
		if err := json.Unmarshal(v, &u.MaxKw); err != nil {
			return err
		}
	}
	if v, ok := raw["serial"]; ok {
		u.serialSet = true
		if err := json.Unmarshal(v, &u.Serial); err != nil {
			return err
		}
	}
	if v, ok := raw["grid_x"]; ok {
		u.gridXSet = true
		if err := json.Unmarshal(v, &u.GridX); err != nil {
			return err
		}
	}
	if v, ok := raw["grid_y"]; ok {
		u.gridYSet = true
		if err := json.Unmarshal(v, &u.GridY); err != nil {
			return err
		}
	}
	if v, ok := raw["grid_rotation"]; ok {
		if err := json.Unmarshal(v, &u.GridRotation); err != nil {
			return err
		}
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
	if req.UHeight != nil && (*req.UHeight < 1 || *req.UHeight > 60) {
		httpx.Error(w, http.StatusBadRequest, "u_height must be between 1 and 60")
		return
	}
	if (req.GridX != nil && *req.GridX < 0) || (req.GridY != nil && *req.GridY < 0) {
		httpx.Error(w, http.StatusBadRequest, "grid_x and grid_y must be >= 0")
		return
	}
	if req.GridRotation != nil {
		switch *req.GridRotation {
		case 0, 90, 180, 270:
		default:
			httpx.Error(w, http.StatusBadRequest, "grid_rotation must be one of 0, 90, 180, 270")
			return
		}
	}
	// PR 54 ABAC: look up the rack's site and enforce before any work.
	currentRack, err := h.Q.GetRack(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "rack not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	p, _ := auth.From(r.Context())
	if serr := auth.EnforceSiteScope(r.Context(), h.Q, p, currentRack.SiteID, "inventory:racks:update"); serr != nil {
		httpx.Error(w, http.StatusForbidden, serr.Error())
		return
	}
	// Shrink check: if u_height is decreasing, refuse if any placed
	// asset would fall outside the new envelope.
	if req.UHeight != nil {
		if *req.UHeight < currentRack.UHeight {
			placed, err := h.Q.GetRackAssetsForShrinkCheck(r.Context(), id)
			if err == nil {
				var offenders []string
				for _, p := range placed {
					if p.RackPositionU == nil {
						continue
					}
					units := int32(1)
					if p.RackUnits != nil {
						units = *p.RackUnits
					}
					top := *p.RackPositionU + units - 1
					if top > *req.UHeight {
						offenders = append(offenders, p.Name)
					}
				}
				if len(offenders) > 0 {
					httpx.Error(w, http.StatusConflict, fmt.Sprintf("cannot shrink rack: placed assets exceed new envelope: %v", offenders))
					return
				}
			}
		}
	}
	out, err := h.Q.UpdateRack(r.Context(), dbq.UpdateRackParams{
		ID: id, Name: req.Name, UHeight: req.UHeight,
		MaxKwSet: req.maxKwSet, MaxKw: req.MaxKw,
		SerialSet: req.serialSet, Serial: req.Serial,
		GridXSet: req.gridXSet, GridX: req.GridX,
		GridYSet: req.gridYSet, GridY: req.GridY,
		GridRotation: req.GridRotation,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "rack not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	sid := out.SiteID
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "rack.update", TargetType: "rack", TargetID: id.String(), SiteID: &sid,
	})
	httpx.JSON(w, http.StatusOK, out)
}
