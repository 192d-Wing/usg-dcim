// Package ipam holds GET handlers for IPAM resources: fabrics,
// supernets, vrfs, subnets, addresses, overlays, vnis, vteps,
// vtep-memberships, dhcp/servers. Computed endpoints (utilization,
// free-space) deferred — they're domain logic, not straight SELECTs.
// Writes still served by Python otter until Phase 2.
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

	ListFabrics(ctx context.Context, arg dbq.ListFabricsParams) ([]dbq.Fabric, error)
	CountFabrics(ctx context.Context, arg dbq.CountFabricsParams) (int64, error)
	GetFabric(ctx context.Context, id uuid.UUID) (dbq.Fabric, error)
	ListSupernets(ctx context.Context, arg dbq.ListSupernetsParams) ([]dbq.Supernet, error)
	CountSupernets(ctx context.Context, arg dbq.CountSupernetsParams) (int64, error)
	GetSupernet(ctx context.Context, id uuid.UUID) (dbq.Supernet, error)

	ListOverlays(ctx context.Context, arg dbq.ListOverlaysParams) ([]dbq.Overlay, error)
	CountOverlays(ctx context.Context, arg dbq.CountOverlaysParams) (int64, error)
	ListVnis(ctx context.Context, arg dbq.ListVnisParams) ([]dbq.Vni, error)
	CountVnis(ctx context.Context, arg dbq.CountVnisParams) (int64, error)
	ListVteps(ctx context.Context, arg dbq.ListVtepsParams) ([]dbq.Vtep, error)
	CountVteps(ctx context.Context, arg dbq.CountVtepsParams) (int64, error)
	ListVtepMemberships(ctx context.Context, arg dbq.ListVtepMembershipsParams) ([]dbq.VtepVniMembership, error)
	CountVtepMemberships(ctx context.Context, arg dbq.CountVtepMembershipsParams) (int64, error)
	ListDhcpServers(ctx context.Context, arg dbq.ListDhcpServersParams) ([]dbq.DhcpServer, error)
	CountDhcpServers(ctx context.Context, arg dbq.CountDhcpServersParams) (int64, error)
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
		r.Get("/fabrics", h.listFabrics)
		r.Get("/fabrics/{id}", h.getFabric)
		r.Get("/supernets", h.listSupernets)
		r.Get("/supernets/{id}", h.getSupernet)
		r.Get("/overlays", h.listOverlays)
		r.Get("/vnis", h.listVnis)
		r.Get("/vteps", h.listVteps)
		r.Get("/vtep-memberships", h.listVtepMemberships)
		r.Get("/dhcp/servers", h.listDhcpServers)
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

// ---- Fabrics ----

type fabricsPage struct {
	Items  []dbq.Fabric `json:"items"`
	Total  int64        `json:"total"`
	Limit  int32        `json:"limit"`
	Offset int32        `json:"offset"`
}

func (h *Handler) listFabrics(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(pageSize(q), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
	params := dbq.ListFabricsParams{Limit: limit, Offset: offset, Enclave: strPtr(q.Get("enclave"))}
	items, err := h.Q.ListFabrics(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountFabrics(r.Context(), dbq.CountFabricsParams{Enclave: params.Enclave})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, fabricsPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

func (h *Handler) getFabric(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	f, err := h.Q.GetFabric(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "fabric not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, f)
}

// ---- Supernets ----

type supernetsPage struct {
	Items  []dbq.Supernet `json:"items"`
	Total  int64          `json:"total"`
	Limit  int32          `json:"limit"`
	Offset int32          `json:"offset"`
}

// parentFilter computes the (mode, id) pair the SQL CASE expects.
// Python semantics:
//   - top_level=true                       → mode='null'
//   - parent_supernet_id="null" (literal)  → mode='null'
//   - parent_supernet_id=<uuid>            → mode='eq', id=<uuid>
//   - neither                              → mode='any'
// top_level wins if both are present.
func parentFilter(q map[string][]string) (mode string, id *uuid.UUID, err error) {
	if first(q, "top_level") == "true" || first(q, "top_level") == "1" {
		return "null", nil, nil
	}
	raw := first(q, "parent_supernet_id")
	switch raw {
	case "":
		return "any", nil, nil
	case "null":
		return "null", nil, nil
	}
	u, parseErr := uuid.Parse(raw)
	if parseErr != nil {
		return "", nil, parseErr
	}
	return "eq", &u, nil
}

func (h *Handler) listSupernets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(pageSize(q), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
	mode, parentID, err := parentFilter(q)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "parent_supernet_id is not a uuid or 'null'")
		return
	}
	params := dbq.ListSupernetsParams{
		Limit: limit, Offset: offset,
		ParentFilterMode: mode, ParentSupernetID: parentID,
	}
	if v := q.Get("fabric_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "fabric_id is not a uuid")
			return
		}
		params.FabricID = &id
	}
	if v := q.Get("vrf_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "vrf_id is not a uuid")
			return
		}
		params.VrfID = &id
	}
	items, err := h.Q.ListSupernets(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountSupernets(r.Context(), dbq.CountSupernetsParams{
		FabricID: params.FabricID, VrfID: params.VrfID,
		ParentFilterMode: params.ParentFilterMode, ParentSupernetID: params.ParentSupernetID,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, supernetsPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

func (h *Handler) getSupernet(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	s, err := h.Q.GetSupernet(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "supernet not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, s)
}

// ---- Overlays ----

type overlaysPage struct {
	Items  []dbq.Overlay `json:"items"`
	Total  int64         `json:"total"`
	Limit  int32         `json:"limit"`
	Offset int32         `json:"offset"`
}

func (h *Handler) listOverlays(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(pageSize(q), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
	params := dbq.ListOverlaysParams{Limit: limit, Offset: offset}
	if v := q.Get("fabric_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "fabric_id is not a uuid")
			return
		}
		params.FabricID = &id
	}
	items, err := h.Q.ListOverlays(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountOverlays(r.Context(), dbq.CountOverlaysParams{FabricID: params.FabricID})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, overlaysPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

// ---- VNIs ----

type vnisPage struct {
	Items  []dbq.Vni `json:"items"`
	Total  int64     `json:"total"`
	Limit  int32     `json:"limit"`
	Offset int32     `json:"offset"`
}

func (h *Handler) listVnis(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(pageSize(q), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
	params := dbq.ListVnisParams{Limit: limit, Offset: offset, Kind: strPtr(q.Get("kind"))}
	for _, f := range []struct {
		key string
		dst **uuid.UUID
	}{
		{"overlay_id", &params.OverlayID},
		{"fabric_id", &params.FabricID},
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
	items, err := h.Q.ListVnis(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountVnis(r.Context(), dbq.CountVnisParams{
		OverlayID: params.OverlayID, FabricID: params.FabricID, Kind: params.Kind,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, vnisPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

// ---- VTEPs ----

type vtepsPage struct {
	Items  []dbq.Vtep `json:"items"`
	Total  int64      `json:"total"`
	Limit  int32      `json:"limit"`
	Offset int32      `json:"offset"`
}

func (h *Handler) listVteps(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(pageSize(q), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
	params := dbq.ListVtepsParams{Limit: limit, Offset: offset}
	for _, f := range []struct {
		key string
		dst **uuid.UUID
	}{
		{"overlay_id", &params.OverlayID},
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
	items, err := h.Q.ListVteps(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountVteps(r.Context(), dbq.CountVtepsParams{OverlayID: params.OverlayID, AssetID: params.AssetID})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, vtepsPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

// ---- VTEP/VNI memberships ----

type membershipsPage struct {
	Items  []dbq.VtepVniMembership `json:"items"`
	Total  int64                   `json:"total"`
	Limit  int32                   `json:"limit"`
	Offset int32                   `json:"offset"`
}

func (h *Handler) listVtepMemberships(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(pageSize(q), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
	params := dbq.ListVtepMembershipsParams{Limit: limit, Offset: offset}
	for _, f := range []struct {
		key string
		dst **uuid.UUID
	}{
		{"vtep_id", &params.VtepID},
		{"vni_id", &params.VniID},
		{"overlay_id", &params.OverlayID},
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
	items, err := h.Q.ListVtepMemberships(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountVtepMemberships(r.Context(), dbq.CountVtepMembershipsParams{
		VtepID: params.VtepID, VniID: params.VniID, OverlayID: params.OverlayID,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, membershipsPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

// ---- DHCP servers ----

type dhcpServersPage struct {
	Items  []dbq.DhcpServer `json:"items"`
	Total  int64            `json:"total"`
	Limit  int32            `json:"limit"`
	Offset int32            `json:"offset"`
}

func (h *Handler) listDhcpServers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(pageSize(q), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
	params := dbq.ListDhcpServersParams{Limit: limit, Offset: offset}
	if v := q.Get("fabric_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "fabric_id is not a uuid")
			return
		}
		params.FabricID = &id
	}
	items, err := h.Q.ListDhcpServers(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountDhcpServers(r.Context(), dbq.CountDhcpServersParams{FabricID: params.FabricID})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, dhcpServersPage{Items: items, Total: total, Limit: limit, Offset: offset})
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
