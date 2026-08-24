// Package racks holds GET handlers for /api/v1/racks. List + get; the
// Python POST/PATCH still serve writes until Phase 2.
package racks

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

const capRacksRead = "inventory:racks:read"

type Querier interface {
	ListRacks(ctx context.Context, arg dbq.ListRacksParams) ([]dbq.Rack, error)
	CountRacks(ctx context.Context, arg dbq.CountRacksParams) (int64, error)
	GetRack(ctx context.Context, id uuid.UUID) (dbq.Rack, error)
	CreateRack(ctx context.Context, arg dbq.CreateRackParams) (dbq.Rack, error)
	UpdateRack(ctx context.Context, arg dbq.UpdateRackParams) (dbq.Rack, error)
	GetRackAssetsForShrinkCheck(ctx context.Context, rackID uuid.UUID) ([]dbq.GetRackAssetsForShrinkCheckRow, error)

	// Hard delete (UX-debt batch). Decommission remains the asset
	// lifecycle path; rack DELETE is for mistakes and test hygiene.
	CountAssetsInRack(ctx context.Context, rackID *uuid.UUID) (int64, error)
	DeleteRack(ctx context.Context, id uuid.UUID) (int64, error)

	// PR 54: ABAC SiteMatches expansion.
	GetSiteRegionID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetSiteOrganizationID(ctx context.Context, id uuid.UUID) (*uuid.UUID, error)
	ListSiteGroupIDsForSite(ctx context.Context, siteID uuid.UUID) ([]uuid.UUID, error)

	// PR 62: scope-filtered LISTs.
	ListSiteIDsForExpansion(ctx context.Context, arg dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error)
}

type Handler struct {
	Q     Querier
	Audit audit.Recorder
}

func (h *Handler) Mount(r chi.Router) {
	// Read paths gated by inventory:racks:read — see sites/Mount for
	// why ScopedSiteFilter alone doesn't keep cap-less principals out.
	r.With(auth.RequireCapability(capRacksRead)).Get("/racks", h.list)
	r.With(auth.RequireCapability(capRacksRead)).Get("/racks/{id}", h.get)
	r.With(auth.RequireCapability("inventory:racks:create")).Post("/racks", h.create)
	r.With(auth.RequireCapability("inventory:racks:update")).Patch("/racks/{id}", h.update)
	r.With(auth.RequireCapability("inventory:racks:delete")).Delete("/racks/{id}", h.delete)
}

type listResponse = httpx.Page[dbq.Rack]

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	p, _ := auth.From(r.Context())
	scopeSiteIds, scoped, err := auth.ScopedSiteFilter(r.Context(), h.Q, p, capRacksRead)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if scoped && len(scopeSiteIds) == 0 {
		httpx.JSON(w, http.StatusOK, httpx.EmptyPage[dbq.Rack](limit, offset))
		return
	}
	params := dbq.ListRacksParams{Limit: limit, Offset: offset, ScopeSiteIds: scopeSiteIds}
	if v := q.Get("site_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "site_id is not a uuid")
			return
		}
		params.SiteID = &id
	}
	if v := q.Get("row_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "row_id is not a uuid")
			return
		}
		params.RowID = &id
	}
	items, err := h.Q.ListRacks(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountRacks(r.Context(), dbq.CountRacksParams{SiteID: params.SiteID, RowID: params.RowID, ScopeSiteIds: scopeSiteIds})
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
	rack, err := h.Q.GetRack(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "rack not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	// Per-row ABAC: scoped principals can't read racks outside scope.
	p, _ := auth.From(r.Context())
	if serr := auth.EnforceSiteScope(r.Context(), h.Q, p, rack.SiteID, capRacksRead); serr != nil {
		status, msg := httpx.Mapped(serr)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, rack)
}
