// Package collectors serves /api/v1/collectors: list/get plus the
// enroll + heartbeat write paths and config/enabled/decommission
// mutations. Wire shapes, capability codes, and audit-event shapes
// match Python's api/collectors.py so the cutover flips ingress
// without observable behavior change.
package collectors

import (
	"context"
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

const (
	capCollectorsUpdate = "collectors:collectors:update"
	capCollectorsEnroll = "collectors:collectors:enroll"
	capIngestWrite      = "collectors:ingest:write"
)

type Querier interface {
	ListCollectors(ctx context.Context, arg dbq.ListCollectorsParams) ([]dbq.Collector, error)
	CountCollectors(ctx context.Context, arg dbq.CountCollectorsParams) (int64, error)
	GetCollector(ctx context.Context, id uuid.UUID) (dbq.Collector, error)
	EnrollCollector(ctx context.Context, arg dbq.EnrollCollectorParams) (dbq.EnrollCollectorRow, error)
	HeartbeatCollector(ctx context.Context, arg dbq.HeartbeatCollectorParams) ([]byte, error)
	InsertCollectorHeartbeat(ctx context.Context, arg dbq.InsertCollectorHeartbeatParams) error
	SetCollectorConfigOverrides(ctx context.Context, arg dbq.SetCollectorConfigOverridesParams) (dbq.Collector, error)
	SetCollectorEnabled(ctx context.Context, arg dbq.SetCollectorEnabledParams) (dbq.Collector, error)
	DecommissionCollector(ctx context.Context, id uuid.UUID) (dbq.Collector, error)

	// EnforceSiteScope dependencies — required so enroll can refuse a
	// site outside the operator's scope.
	GetSiteRegionID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetSiteOrganizationID(ctx context.Context, id uuid.UUID) (*uuid.UUID, error)
	ListSiteGroupIDsForSite(ctx context.Context, siteID uuid.UUID) ([]uuid.UUID, error)
}

type Handler struct {
	Q     Querier
	Audit audit.Recorder
}

func (h *Handler) Mount(r chi.Router) {
	r.Route("/collectors", func(r chi.Router) {
		r.Get("/", h.list)
		r.Get("/{id}", h.get)
		r.With(auth.RequireCapability(capCollectorsEnroll)).Post("/enroll", h.enroll)
		r.With(auth.RequireCapability(capIngestWrite)).Post("/{id}/heartbeat", h.heartbeat)
		r.With(auth.RequireCapability(capCollectorsUpdate)).Patch("/{id}/config", h.patchConfig)
		r.With(auth.RequireCapability(capCollectorsUpdate)).Patch("/{id}/enabled", h.patchEnabled)
		r.With(auth.RequireCapability(capCollectorsUpdate)).Delete("/{id}", h.decommission)
	})
}

type listResponse struct {
	Items  []dbq.Collector `json:"items"`
	Total  int64           `json:"total"`
	Limit  int32           `json:"limit"`
	Offset int32           `json:"offset"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)

	params := dbq.ListCollectorsParams{
		Limit:  limit,
		Offset: offset,
		Status: strPtr(q.Get("status")),
	}
	if v := q.Get("site_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "site_id is not a uuid")
			return
		}
		params.SiteID = &id
	}

	items, err := h.Q.ListCollectors(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountCollectors(r.Context(), dbq.CountCollectorsParams{
		SiteID: params.SiteID, Status: params.Status,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse{Items: items, Total: total, Limit: limit, Offset: offset})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	obj, err := h.Q.GetCollector(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "collector not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, obj)
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
