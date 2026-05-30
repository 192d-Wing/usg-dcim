// Package cables holds GET handlers for /api/v1/cables.
package cables

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

type Querier interface {
	ListCables(ctx context.Context, arg dbq.ListCablesParams) ([]dbq.Cable, error)
	CountCables(ctx context.Context, arg dbq.CountCablesParams) (int64, error)
	GetCable(ctx context.Context, id uuid.UUID) (dbq.Cable, error)
	CreateCable(ctx context.Context, arg dbq.CreateCableParams) (dbq.Cable, error)
	UpdateCable(ctx context.Context, arg dbq.UpdateCableParams) (dbq.Cable, error)
	DeleteCable(ctx context.Context, id uuid.UUID) error
	GetAssetSiteID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetAsset(ctx context.Context, id uuid.UUID) (dbq.Asset, error)
	FindCableForPort(ctx context.Context, arg dbq.FindCableForPortParams) (dbq.FindCableForPortRow, error)
	// SiteScope methods so the handler can post-fetch enforce_site_scope
	// equivalents on every mutation (matching Python's behavior after
	// PR #195).
	GetSiteRegionID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetSiteOrganizationID(ctx context.Context, id uuid.UUID) (*uuid.UUID, error)
	ListSiteGroupIDsForSite(ctx context.Context, siteID uuid.UUID) ([]uuid.UUID, error)
	// Site-set expansion for the ScopedSiteFilter list-clamp.
	ListSiteIDsForExpansion(ctx context.Context, arg dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error)
}

type Handler struct {
	Q     Querier
	Audit audit.Recorder
}

func (h *Handler) Mount(r chi.Router) {
	// Read paths gated by inventory:cables:read — see sites/Mount for
	// why ScopedSiteFilter alone doesn't keep cap-less principals out.
	r.With(auth.RequireCapability(capCablesRead)).Get("/cables", h.list)
	r.With(auth.RequireCapability(capCablesRead)).Get(cableByID, h.get)
	r.With(auth.RequireCapability("inventory:cables:create")).Post("/cables", h.create)
	r.With(auth.RequireCapability("inventory:cables:update")).Patch(cableByID, h.update)
	r.With(auth.RequireCapability("inventory:cables:delete")).Delete(cableByID, h.delete)
}

const (
	capCablesRead = "inventory:cables:read"
	cableByID     = "/cables/{id}"
)

type listResponse struct {
	Items  []dbq.Cable `json:"items"`
	Total  int64       `json:"total"`
	Limit  int32       `json:"limit"`
	Offset int32       `json:"offset"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	params := dbq.ListCablesParams{Limit: limit, Offset: offset}
	for _, f := range []struct {
		key string
		dst **uuid.UUID
	}{
		{"site_id", &params.SiteID},
		{"asset_id", &params.AssetID},
		{"rack_id", &params.RackID},
	} {
		if v := q.Get(f.key); v != "" {
			id, err := uuid.Parse(v)
			if err != nil {
				httpx.Error(w, http.StatusBadRequest, f.key+" is not a uuid")
				return
			}
			*f.dst = &id
		}
	}
	// ABAC: scoped principals see only cables whose site_id falls in
	// their reachable site set. Mirrors Python's
	// scope_filtered_site_ids on inventory:cables:read.
	p, _ := auth.From(r.Context())
	scopeSiteIds, scoped, err := auth.ScopedSiteFilter(r.Context(), h.Q, p, capCablesRead)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if scoped && len(scopeSiteIds) == 0 {
		httpx.JSON(w, http.StatusOK, listResponse{Items: []dbq.Cable{}, Total: 0, Limit: limit, Offset: offset})
		return
	}
	if scoped {
		params.ScopeSiteIds = scopeSiteIds
	}
	items, err := h.Q.ListCables(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountCables(r.Context(), dbq.CountCablesParams{
		SiteID: params.SiteID, AssetID: params.AssetID, RackID: params.RackID,
		ScopeSiteIds: params.ScopeSiteIds,
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
	cable, err := h.Q.GetCable(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "cable not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	// Per-row ABAC: scoped principals can't read cables outside scope.
	p, _ := auth.From(r.Context())
	if serr := auth.EnforceSiteScope(r.Context(), h.Q, p, cable.SiteID, capCablesRead); serr != nil {
		status, msg := httpx.Mapped(serr)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, cable)
}
