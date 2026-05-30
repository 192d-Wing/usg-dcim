package locations

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

const badBody = "bad request body"

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

// ----------------------- PATCH / DELETE -----------------------

// buildingUpdateReq tracks name/code; ABAC happens before the SQL
// runs via SiteIDForBuilding (which is just GetBuilding(...).SiteID).
type buildingUpdateReq struct {
	Name *string `json:"name"`
	Code *string `json:"code"`
}

type roomUpdateReq struct {
	Name             *string `json:"name"`
	Code             *string `json:"code"`
	FloorAreaSqftSet bool    `json:"-"`
	FloorAreaSqft    *int32  `json:"floor_area_sqft"`
}

func (u *roomUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["name"]; ok {
		if err := json.Unmarshal(v, &u.Name); err != nil {
			return err
		}
	}
	if v, ok := raw["code"]; ok {
		if err := json.Unmarshal(v, &u.Code); err != nil {
			return err
		}
	}
	if v, ok := raw["floor_area_sqft"]; ok {
		u.FloorAreaSqftSet = true
		if err := json.Unmarshal(v, &u.FloorAreaSqft); err != nil {
			return err
		}
	}
	return nil
}

type rowUpdateReq struct {
	Name *string `json:"name"`
	Code *string `json:"code"`
}

func writeMapped(w http.ResponseWriter, err error) {
	status, msg := httpx.Mapped(err)
	httpx.Error(w, status, msg)
}

func parseID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) updateBuilding(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	existing, err := h.Q.GetBuilding(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "building not found")
			return
		}
		writeMapped(w, err)
		return
	}
	p, _ := auth.From(r.Context())
	if err := auth.EnforceSiteScope(r.Context(), h.Q, p,existing.SiteID, capBuildingsUpdate); err != nil {
		writeMapped(w, err)
		return
	}
	var req buildingUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, badBody)
		return
	}
	out, err := h.Q.UpdateBuilding(r.Context(), dbq.UpdateBuildingParams{ID: id, Name: req.Name, Code: req.Code})
	if err != nil {
		writeMapped(w, err)
		return
	}
	sid := out.SiteID
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "building.update", TargetType: "building", TargetID: out.ID.String(), SiteID: &sid,
		Diff: buildingDiff(req),
	})
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteBuilding(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	existing, err := h.Q.GetBuilding(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "building not found")
			return
		}
		writeMapped(w, err)
		return
	}
	p, _ := auth.From(r.Context())
	if err := auth.EnforceSiteScope(r.Context(), h.Q, p,existing.SiteID, capBuildingsDelete); err != nil {
		writeMapped(w, err)
		return
	}
	if err := h.Q.DeleteBuilding(r.Context(), id); err != nil {
		// FK violations from non-empty buildings surface as 409 to
		// distinguish them from generic 5xx.
		writeMapped(w, err)
		return
	}
	sid := existing.SiteID
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "building.delete", TargetType: "building", TargetID: id.String(), SiteID: &sid,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) updateRoom(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if _, err := h.Q.GetRoom(r.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "room not found")
			return
		}
		writeMapped(w, err)
		return
	}
	siteID, err := h.Q.SiteIDForRoom(r.Context(), id)
	if err != nil {
		writeMapped(w, err)
		return
	}
	p, _ := auth.From(r.Context())
	if err := auth.EnforceSiteScope(r.Context(), h.Q, p,siteID, capRoomsUpdate); err != nil {
		writeMapped(w, err)
		return
	}
	var req roomUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, badBody)
		return
	}
	out, err := h.Q.UpdateRoom(r.Context(), dbq.UpdateRoomParams{
		ID: id, Name: req.Name, Code: req.Code,
		FloorAreaSqftSet: req.FloorAreaSqftSet, FloorAreaSqft: req.FloorAreaSqft,
	})
	if err != nil {
		writeMapped(w, err)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "room.update", TargetType: "room", TargetID: out.ID.String(), SiteID: &siteID,
		Diff: roomDiff(req),
	})
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteRoom(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if _, err := h.Q.GetRoom(r.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "room not found")
			return
		}
		writeMapped(w, err)
		return
	}
	siteID, err := h.Q.SiteIDForRoom(r.Context(), id)
	if err != nil {
		writeMapped(w, err)
		return
	}
	p, _ := auth.From(r.Context())
	if err := auth.EnforceSiteScope(r.Context(), h.Q, p,siteID, capRoomsDelete); err != nil {
		writeMapped(w, err)
		return
	}
	if err := h.Q.DeleteRoom(r.Context(), id); err != nil {
		writeMapped(w, err)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "room.delete", TargetType: "room", TargetID: id.String(), SiteID: &siteID,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) updateRow(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if _, err := h.Q.GetRow(r.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "row not found")
			return
		}
		writeMapped(w, err)
		return
	}
	siteID, err := h.Q.SiteIDForRow(r.Context(), id)
	if err != nil {
		writeMapped(w, err)
		return
	}
	p, _ := auth.From(r.Context())
	if err := auth.EnforceSiteScope(r.Context(), h.Q, p,siteID, capRowsUpdate); err != nil {
		writeMapped(w, err)
		return
	}
	var req rowUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, badBody)
		return
	}
	out, err := h.Q.UpdateRow(r.Context(), dbq.UpdateRowParams{ID: id, Name: req.Name, Code: req.Code})
	if err != nil {
		writeMapped(w, err)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "row.update", TargetType: "row", TargetID: out.ID.String(), SiteID: &siteID,
		Diff: rowDiff(req),
	})
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteRow(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if _, err := h.Q.GetRow(r.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "row not found")
			return
		}
		writeMapped(w, err)
		return
	}
	siteID, err := h.Q.SiteIDForRow(r.Context(), id)
	if err != nil {
		writeMapped(w, err)
		return
	}
	p, _ := auth.From(r.Context())
	if err := auth.EnforceSiteScope(r.Context(), h.Q, p,siteID, capRowsDelete); err != nil {
		writeMapped(w, err)
		return
	}
	if err := h.Q.DeleteRow(r.Context(), id); err != nil {
		writeMapped(w, err)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "row.delete", TargetType: "row", TargetID: id.String(), SiteID: &siteID,
	})
	w.WriteHeader(http.StatusNoContent)
}

func buildingDiff(req buildingUpdateReq) map[string]any {
	d := map[string]any{}
	if req.Name != nil {
		d["name"] = *req.Name
	}
	if req.Code != nil {
		d["code"] = *req.Code
	}
	return d
}

func roomDiff(req roomUpdateReq) map[string]any {
	d := map[string]any{}
	if req.Name != nil {
		d["name"] = *req.Name
	}
	if req.Code != nil {
		d["code"] = *req.Code
	}
	if req.FloorAreaSqftSet {
		d["floor_area_sqft"] = req.FloorAreaSqft
	}
	return d
}

func rowDiff(req rowUpdateReq) map[string]any {
	d := map[string]any{}
	if req.Name != nil {
		d["name"] = *req.Name
	}
	if req.Code != nil {
		d["code"] = *req.Code
	}
	return d
}
