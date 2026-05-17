package notifications

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

func idFromURL(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return uuid.Nil, false
	}
	return id, true
}

func mapErr(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "channel not found")
		return
	}
	status, msg := httpx.Mapped(err)
	httpx.Error(w, status, msg)
}

type channelCreateReq struct {
	Name            string          `json:"name"`
	Kind            string          `json:"kind"`
	ConfigJson      json.RawMessage `json:"config_json"`
	MinSeverity     string          `json:"min_severity"`
	NotifyOnFire    *bool           `json:"notify_on_fire"`
	NotifyOnResolve *bool           `json:"notify_on_resolve"`
	Enabled         *bool           `json:"enabled"`
}

func (h *Handler) createChannel(w http.ResponseWriter, r *http.Request) {
	var req channelCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.Kind == "" {
		httpx.Error(w, http.StatusBadRequest, "name and kind required")
		return
	}
	if req.MinSeverity == "" {
		req.MinSeverity = "warning"
	}
	fire := true
	if req.NotifyOnFire != nil {
		fire = *req.NotifyOnFire
	}
	resolve := true
	if req.NotifyOnResolve != nil {
		resolve = *req.NotifyOnResolve
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	out, err := h.Q.CreateNotificationChannel(r.Context(), dbq.CreateNotificationChannelParams{
		Name: req.Name, Kind: req.Kind, ConfigJson: req.ConfigJson,
		MinSeverity: req.MinSeverity, NotifyOnFire: fire, NotifyOnResolve: resolve, Enabled: enabled,
	})
	if err != nil {
		mapErr(w, err)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "notification_channel.create", TargetType: "notification_channel", TargetID: out.ID.String()})
	httpx.JSON(w, http.StatusCreated, out)
}

type channelUpdateReq struct {
	Name            *string         `json:"name,omitempty"`
	ConfigJson      json.RawMessage `json:"config_json,omitempty"`
	MinSeverity     *string         `json:"min_severity,omitempty"`
	NotifyOnFire    *bool           `json:"notify_on_fire,omitempty"`
	NotifyOnResolve *bool           `json:"notify_on_resolve,omitempty"`
	Enabled         *bool           `json:"enabled,omitempty"`
}

func (h *Handler) updateChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	var req channelUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdateNotificationChannel(r.Context(), dbq.UpdateNotificationChannelParams{
		ID: id, Name: req.Name, ConfigJson: req.ConfigJson, MinSeverity: req.MinSeverity,
		NotifyOnFire: req.NotifyOnFire, NotifyOnResolve: req.NotifyOnResolve, Enabled: req.Enabled,
	})
	if err != nil {
		mapErr(w, err)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "notification_channel.update", TargetType: "notification_channel", TargetID: id.String()})
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if err := h.Q.DeleteNotificationChannel(r.Context(), id); err != nil {
		mapErr(w, err)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "notification_channel.delete", TargetType: "notification_channel", TargetID: id.String()})
	w.WriteHeader(http.StatusNoContent)
}
