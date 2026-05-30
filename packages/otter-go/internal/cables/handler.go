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
	DeleteCable(ctx context.Context, id uuid.UUID) error
	GetAssetSiteID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetAsset(ctx context.Context, id uuid.UUID) (dbq.Asset, error)
	FindCableForPort(ctx context.Context, arg dbq.FindCableForPortParams) (dbq.FindCableForPortRow, error)
}

type Handler struct {
	Q     Querier
	Audit audit.Recorder
}

func (h *Handler) Mount(r chi.Router) {
	r.Get("/cables", h.list)
	r.Get("/cables/{id}", h.get)
	r.With(auth.RequireCapability("inventory:cables:create")).Post("/cables", h.create)
	r.With(auth.RequireCapability("inventory:cables:delete")).Delete("/cables/{id}", h.delete)
}

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
	items, err := h.Q.ListCables(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountCables(r.Context(), dbq.CountCablesParams{
		SiteID: params.SiteID, AssetID: params.AssetID, RackID: params.RackID,
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
	httpx.JSON(w, http.StatusOK, cable)
}
