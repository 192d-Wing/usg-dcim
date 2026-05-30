// Package organization holds GET handlers for /api/v1/organizations.
// Mutations still served by Python otter (need audit + ASN-FK conflict check).
package organization

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
	ListOrganizations(ctx context.Context, arg dbq.ListOrganizationsParams) ([]dbq.Organization, error)
	CountOrganizations(ctx context.Context) (int64, error)
	GetOrganization(ctx context.Context, id uuid.UUID) (dbq.Organization, error)
	CreateOrganization(ctx context.Context, arg dbq.CreateOrganizationParams) (dbq.Organization, error)
	UpdateOrganization(ctx context.Context, arg dbq.UpdateOrganizationParams) (dbq.Organization, error)
	CountAsnsForOrganization(ctx context.Context, orgID uuid.UUID) (int64, error)
	DeleteOrganization(ctx context.Context, id uuid.UUID) error
}

type Handler struct {
	Q     Querier
	Audit audit.Recorder
}

func (h *Handler) Mount(r chi.Router) {
	r.Route("/organizations", func(r chi.Router) {
		r.Get("/", h.list)
		r.Get("/{id}", h.get)
		r.With(auth.RequireCapability("inventory:organizations:create")).Post("/", h.create)
		r.With(auth.RequireCapability("inventory:organizations:update")).Patch("/{id}", h.update)
		r.With(auth.RequireCapability("inventory:organizations:delete")).Delete("/{id}", h.delete)
	})
}

type listResponse struct {
	Items  []dbq.Organization `json:"items"`
	Total  int64              `json:"total"`
	Limit  int32              `json:"limit"`
	Offset int32              `json:"offset"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	items, err := h.Q.ListOrganizations(r.Context(), dbq.ListOrganizationsParams{Limit: limit, Offset: offset})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountOrganizations(r.Context())
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
	obj, err := h.Q.GetOrganization(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "organization not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, obj)
}

