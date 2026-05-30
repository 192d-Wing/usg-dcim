// Package assets holds the /api/v1/assets surface. PR 41 ported the
// reads + single-row mutations (create, patch, decommission). PR 69
// closed the last gap with bulk upsert. Python otter parity reached.
package assets

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
	ListAssets(ctx context.Context, arg dbq.ListAssetsParams) ([]dbq.Asset, error)
	CountAssets(ctx context.Context, arg dbq.CountAssetsParams) (int64, error)
	GetAsset(ctx context.Context, id uuid.UUID) (dbq.Asset, error)
	CreateAsset(ctx context.Context, arg dbq.CreateAssetParams) (dbq.Asset, error)
	UpdateAsset(ctx context.Context, arg dbq.UpdateAssetParams) (dbq.Asset, error)
	SetAssetDecommissioned(ctx context.Context, id uuid.UUID) (dbq.Asset, error)
	FindAssetByManufacturerSerial(ctx context.Context, manufacturer, serial string) (dbq.Asset, error)
	CountConsumerPowerDrops(ctx context.Context, assetID uuid.UUID) (int64, error)
	CountPduPowerDrops(ctx context.Context, pduAssetID uuid.UUID) (int64, error)
	ListDownstreamAssetNames(ctx context.Context, pduAssetID uuid.UUID) ([]string, error)
	DeleteConsumerPowerConnections(ctx context.Context, assetID uuid.UUID) error
	DeletePduPowerConnections(ctx context.Context, pduAssetID uuid.UUID) error

	// Placement invariants (PR 51)
	GetRack(ctx context.Context, id uuid.UUID) (dbq.Rack, error)
	ListRackAssetsForPlacement(ctx context.Context, arg dbq.ListRackAssetsForPlacementParams) ([]dbq.RackPlacementRow, error)

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
	r.Get("/assets", h.list)
	r.Get("/assets/{id}", h.get)
	r.With(auth.RequireCapability("inventory:assets:read")).Get("/assets/{id}/decommission/preview", h.decommissionPreview)
	r.With(auth.RequireCapability("inventory:assets:create")).Post("/assets", h.create)
	r.With(auth.RequireCapability("inventory:assets:update")).Patch("/assets/{id}", h.update)
	r.With(auth.RequireCapability("inventory:assets:update")).Post("/assets/{id}/decommission", h.decommission)
	r.With(auth.RequireCapability("inventory:bulk:execute")).Post("/assets/bulk", h.bulkUpsert)
}

type listResponse = httpx.Page[dbq.Asset]

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	p, _ := auth.From(r.Context())
	scopeSiteIds, scoped, err := auth.ScopedSiteFilter(r.Context(), h.Q, p, "inventory:assets:read")
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if scoped && len(scopeSiteIds) == 0 {
		httpx.JSON(w, http.StatusOK, httpx.EmptyPage[dbq.Asset](limit, offset))
		return
	}
	params := dbq.ListAssetsParams{
		Limit:          limit,
		Offset:         offset,
		Kind:           strPtr(q.Get("kind")),
		LifecycleState: strPtr(q.Get("lifecycle_state")),
		Serial:         strPtr(q.Get("serial")),
		Hostname:       strPtr(q.Get("hostname")),
		ScopeSiteIds:   scopeSiteIds,
	}
	if v := q.Get("site_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "site_id is not a uuid")
			return
		}
		params.SiteID = &id
	}
	if v := q.Get("rack_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "rack_id is not a uuid")
			return
		}
		params.RackID = &id
	}
	items, err := h.Q.ListAssets(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountAssets(r.Context(), dbq.CountAssetsParams{
		SiteID: params.SiteID, RackID: params.RackID, Kind: params.Kind,
		LifecycleState: params.LifecycleState, Serial: params.Serial, Hostname: params.Hostname,
		ScopeSiteIds: scopeSiteIds,
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
	asset, err := h.Q.GetAsset(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "asset not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, asset)
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
