package locations

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type buildingReq struct {
	SiteID uuid.UUID `json:"site_id"`
	Name   string    `json:"name"`
	Code   string    `json:"code"`
}

func (h *Handler) createBuilding(w http.ResponseWriter, r *http.Request) {
	var req buildingReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.Code == "" || req.SiteID == uuid.Nil {
		httpx.Error(w, http.StatusBadRequest, "site_id, name, code required")
		return
	}
	out, err := h.Q.CreateBuilding(r.Context(), dbq.CreateBuildingParams{
		SiteID: req.SiteID, Name: req.Name, Code: req.Code,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	sid := out.SiteID
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "building.create", TargetType: "building", TargetID: out.ID.String(), SiteID: &sid,
	})
	httpx.JSON(w, http.StatusCreated, out)
}

type roomReq struct {
	BuildingID    uuid.UUID `json:"building_id"`
	Name          string    `json:"name"`
	Code          string    `json:"code"`
	FloorAreaSqft *int32    `json:"floor_area_sqft"`
}

func (h *Handler) createRoom(w http.ResponseWriter, r *http.Request) {
	var req roomReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.Code == "" || req.BuildingID == uuid.Nil {
		httpx.Error(w, http.StatusBadRequest, "building_id, name, code required")
		return
	}
	out, err := h.Q.CreateRoom(r.Context(), dbq.CreateRoomParams{
		BuildingID: req.BuildingID, Name: req.Name, Code: req.Code, FloorAreaSqft: req.FloorAreaSqft,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "room.create", TargetType: "room", TargetID: out.ID.String(),
	})
	httpx.JSON(w, http.StatusCreated, out)
}

type rowReq struct {
	RoomID uuid.UUID `json:"room_id"`
	Name   string    `json:"name"`
	Code   string    `json:"code"`
}

func (h *Handler) createRow(w http.ResponseWriter, r *http.Request) {
	var req rowReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.Code == "" || req.RoomID == uuid.Nil {
		httpx.Error(w, http.StatusBadRequest, "room_id, name, code required")
		return
	}
	out, err := h.Q.CreateRow(r.Context(), dbq.CreateRowParams{
		RoomID: req.RoomID, Name: req.Name, Code: req.Code,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "row.create", TargetType: "row", TargetID: out.ID.String(),
	})
	httpx.JSON(w, http.StatusCreated, out)
}
