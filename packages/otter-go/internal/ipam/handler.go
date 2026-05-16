// Package ipam holds GET handlers for IPAM essentials: vrfs, subnets,
// addresses. Computed endpoints (utilization, free-space) and
// secondary resources (fabrics, supernets, overlays, vnis, vteps,
// dhcp/servers) deferred to a follow-up PR. Writes still served by
// Python otter until Phase 2.
package ipam

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type Querier interface {
	ListVrfs(ctx context.Context, arg dbq.ListVrfsParams) ([]dbq.Vrf, error)
	CountVrfs(ctx context.Context, arg dbq.CountVrfsParams) (int64, error)
	GetVrf(ctx context.Context, id uuid.UUID) (dbq.Vrf, error)
	ListSubnets(ctx context.Context, arg dbq.ListSubnetsParams) ([]dbq.Subnet, error)
	CountSubnets(ctx context.Context, arg dbq.CountSubnetsParams) (int64, error)
	GetSubnet(ctx context.Context, id uuid.UUID) (dbq.Subnet, error)
	ListIPAddresses(ctx context.Context, arg dbq.ListIPAddressesParams) ([]dbq.IPAddress, error)
	CountIPAddresses(ctx context.Context, arg dbq.CountIPAddressesParams) (int64, error)
	GetIPAddress(ctx context.Context, id uuid.UUID) (dbq.IPAddress, error)
}

type Handler struct {
	Q Querier
}

func (h *Handler) Mount(r chi.Router) {
	r.Route("/ipam", func(r chi.Router) {
		r.Get("/vrfs", h.listVrfs)
		r.Get("/vrfs/{id}", h.getVrf)
		r.Get("/subnets", h.listSubnets)
		r.Get("/subnets/{id}", h.getSubnet)
		r.Get("/addresses", h.listAddresses)
		r.Get("/addresses/{id}", h.getAddress)
	})
}

// ---- VRFs ----

type vrfsPage struct {
	Items  []dbq.Vrf `json:"items"`
	Total  int64     `json:"total"`
	Limit  int32     `json:"limit"`
	Offset int32     `json:"offset"`
}

func (h *Handler) listVrfs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(pageSize(q), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
	params := dbq.ListVrfsParams{Limit: limit, Offset: offset}
	if v := q.Get("fabric_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "fabric_id is not a uuid")
			return
		}
		params.FabricID = &id
	}
	items, err := h.Q.ListVrfs(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountVrfs(r.Context(), dbq.CountVrfsParams{FabricID: params.FabricID})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, vrfsPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

func (h *Handler) getVrf(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	v, err := h.Q.GetVrf(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "vrf not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// ---- Subnets ----

type subnetsPage struct {
	Items  []dbq.Subnet `json:"items"`
	Total  int64        `json:"total"`
	Limit  int32        `json:"limit"`
	Offset int32        `json:"offset"`
}

func (h *Handler) listSubnets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(pageSize(q), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
	params := dbq.ListSubnetsParams{Limit: limit, Offset: offset, Purpose: strPtr(q.Get("purpose"))}
	for _, f := range []struct {
		key string
		dst **uuid.UUID
	}{
		{"fabric_id", &params.FabricID},
		{"vrf_id", &params.VrfID},
		{"site_id", &params.SiteID},
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
	items, err := h.Q.ListSubnets(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountSubnets(r.Context(), dbq.CountSubnetsParams{
		FabricID: params.FabricID, VrfID: params.VrfID, SiteID: params.SiteID, Purpose: params.Purpose,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, subnetsPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

func (h *Handler) getSubnet(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	s, err := h.Q.GetSubnet(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "subnet not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, s)
}

// ---- IP Addresses ----

type addressesPage struct {
	Items  []dbq.IPAddress `json:"items"`
	Total  int64           `json:"total"`
	Limit  int32           `json:"limit"`
	Offset int32           `json:"offset"`
}

func (h *Handler) listAddresses(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(pageSize(q), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
	params := dbq.ListIPAddressesParams{
		Limit: limit, Offset: offset,
		Role: strPtr(q.Get("role")), Status: strPtr(q.Get("status")),
	}
	for _, f := range []struct {
		key string
		dst **uuid.UUID
	}{
		{"subnet_id", &params.SubnetID},
		{"asset_id", &params.AssetID},
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
	items, err := h.Q.ListIPAddresses(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountIPAddresses(r.Context(), dbq.CountIPAddressesParams{
		SubnetID: params.SubnetID, AssetID: params.AssetID,
		Role: params.Role, Status: params.Status,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, addressesPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

func (h *Handler) getAddress(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	a, err := h.Q.GetIPAddress(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "ip address not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, a)
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func parseInt32(s string, def, lo, hi int32) int32 {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	v := int32(n)
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func pageSize(q map[string][]string) string {
	if v := first(q, "limit"); v != "" {
		return v
	}
	return first(q, "page_size")
}

func first(q map[string][]string, key string) string {
	if vs := q[key]; len(vs) > 0 {
		return vs[0]
	}
	return ""
}
