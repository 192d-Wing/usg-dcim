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
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
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
	ListAddressStringsInSubnet(ctx context.Context, subnetID uuid.UUID) ([]string, error)
	ListSubnetsForFreeSpace(ctx context.Context, arg dbq.ListSubnetsForFreeSpaceParams) ([]dbq.SubnetForFreeSpaceRow, error)
	ListAddressesInSubnets(ctx context.Context, subnetIDs []uuid.UUID) ([]dbq.AddressInSubnetRow, error)

	ListFabrics(ctx context.Context, arg dbq.ListFabricsParams) ([]dbq.Fabric, error)
	CountFabrics(ctx context.Context, arg dbq.CountFabricsParams) (int64, error)
	GetFabric(ctx context.Context, id uuid.UUID) (dbq.Fabric, error)
	ListSupernets(ctx context.Context, arg dbq.ListSupernetsParams) ([]dbq.Supernet, error)
	CountSupernets(ctx context.Context, arg dbq.CountSupernetsParams) (int64, error)
	GetSupernet(ctx context.Context, id uuid.UUID) (dbq.Supernet, error)
	ListSubnetPrefixesBySupernet(ctx context.Context, supernetID uuid.UUID) ([]string, error)
	ListSupernetsForCarver(ctx context.Context, arg dbq.ListSupernetsForCarverParams) ([]dbq.SupernetForCarverRow, error)
	ListSubnetPrefixesBySupernets(ctx context.Context, supernetIDs []uuid.UUID) ([]dbq.SubnetPrefixBySupernetRow, error)

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

	ListVrfBgpPeers(ctx context.Context, arg dbq.ListVrfBgpPeersParams) ([]dbq.VrfBgpPeer, error)
	CountVrfBgpPeers(ctx context.Context, arg dbq.CountVrfBgpPeersParams) (int64, error)

	// Fabrics
	CreateFabric(ctx context.Context, arg dbq.CreateFabricParams) (dbq.Fabric, error)
	UpdateFabric(ctx context.Context, arg dbq.UpdateFabricParams) (dbq.Fabric, error)
	CountVrfsInFabric(ctx context.Context, fabricID uuid.UUID) (int64, error)
	DeleteFabric(ctx context.Context, id uuid.UUID) error
	// VRFs
	CreateVrf(ctx context.Context, arg dbq.CreateVrfParams) (dbq.Vrf, error)
	UpdateVrf(ctx context.Context, arg dbq.UpdateVrfParams) (dbq.Vrf, error)
	CountSupernetsInVrf(ctx context.Context, vrfID uuid.UUID) (int64, error)
	DeleteVrf(ctx context.Context, id uuid.UUID) error
	// VrfBgpPeers
	CreateVrfBgpPeer(ctx context.Context, arg dbq.CreateVrfBgpPeerParams) (dbq.VrfBgpPeer, error)
	UpdateVrfBgpPeer(ctx context.Context, arg dbq.UpdateVrfBgpPeerParams) (dbq.VrfBgpPeer, error)
	DeleteVrfBgpPeer(ctx context.Context, id uuid.UUID) error
	// Supernets
	CreateSupernet(ctx context.Context, arg dbq.CreateSupernetParams) (dbq.Supernet, error)
	UpdateSupernet(ctx context.Context, arg dbq.UpdateSupernetParams) (dbq.Supernet, error)
	CountSubnetsInSupernet(ctx context.Context, supernetID uuid.UUID) (int64, error)
	DeleteSupernet(ctx context.Context, id uuid.UUID) error
	GetSupernetVrfAndFabric(ctx context.Context, id uuid.UUID) (dbq.SupernetVrfAndFabric, error)
	// Subnets
	CreateSubnet(ctx context.Context, arg dbq.CreateSubnetParams) (dbq.Subnet, error)
	UpdateSubnet(ctx context.Context, arg dbq.UpdateSubnetParams) (dbq.Subnet, error)
	CountAddressesInSubnet(ctx context.Context, subnetID uuid.UUID) (int64, error)
	DeleteSubnet(ctx context.Context, id uuid.UUID) error
	// IPAddresses
	CreateIPAddress(ctx context.Context, arg dbq.CreateIPAddressParams) (dbq.IPAddress, error)
	UpdateIPAddress(ctx context.Context, arg dbq.UpdateIPAddressParams) (dbq.IPAddress, error)
	DeleteIPAddress(ctx context.Context, id uuid.UUID) error
	// Overlays
	CreateOverlay(ctx context.Context, arg dbq.CreateOverlayParams) (dbq.Overlay, error)
	UpdateOverlay(ctx context.Context, arg dbq.UpdateOverlayParams) (dbq.Overlay, error)
	CountVnisInOverlay(ctx context.Context, overlayID uuid.UUID) (int64, error)
	DeleteOverlay(ctx context.Context, id uuid.UUID) error
	// VNIs
	CreateVni(ctx context.Context, arg dbq.CreateVniParams) (dbq.Vni, error)
	UpdateVni(ctx context.Context, arg dbq.UpdateVniParams) (dbq.Vni, error)
	DeleteVni(ctx context.Context, id uuid.UUID) error
	// VTEPs
	CreateVtep(ctx context.Context, arg dbq.CreateVtepParams) (dbq.Vtep, error)
	UpdateVtep(ctx context.Context, arg dbq.UpdateVtepParams) (dbq.Vtep, error)
	DeleteVtep(ctx context.Context, id uuid.UUID) error
	// VTEP/VNI memberships
	CreateVtepMembership(ctx context.Context, arg dbq.CreateVtepMembershipParams) (dbq.VtepVniMembership, error)
	DeleteVtepMembership(ctx context.Context, id uuid.UUID) error
	// DHCP servers
	CreateDhcpServer(ctx context.Context, arg dbq.CreateDhcpServerParams) (dbq.DhcpServer, error)
	UpdateDhcpServer(ctx context.Context, arg dbq.UpdateDhcpServerParams) (dbq.DhcpServer, error)
	DeleteDhcpServer(ctx context.Context, id uuid.UUID) error

	// ABAC parent-fabric lookups. Used by mutation handlers to resolve
	// {id} → fabric_id before EnforceFabricScope. 1-hop lookups shipped
	// in PR 54; PR 55 adds the 2+ hop transitive lookups for subnet
	// (subnets denormalize fabric_id, so still 1-hop in SQL), address
	// (→subnet→fabric), vni (→overlay→fabric), vtep (→overlay→fabric),
	// and vtep-membership (→vtep→overlay→fabric).
	GetVrfFabricID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetOverlayFabricID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetDhcpServerFabricID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetSubnetFabricID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetIPAddressFabricID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetVniFabricID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetVtepFabricID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetVtepMembershipFabricID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
}

type Handler struct {
	Q     Querier
	Audit audit.Recorder
}

// scopedListFilter resolves the caller's fabric scope for capCode and
// returns the slice to pass as ScopeFabricIds on a LIST/COUNT params
// struct, plus an ok flag. ok=false means the principal is fabric-
// scoped but holds zero fabric IDs for capCode — caller must
// short-circuit to an empty page without hitting the DB. On ok=true,
// the slice is nil for a global caller (no filter applied in SQL) or a
// non-nil non-empty slice for a fabric-scoped caller.
func scopedListFilter(r *http.Request, capCode string) (ids []uuid.UUID, ok bool) {
	p, _ := auth.From(r.Context())
	ids, scoped := auth.ScopedFabricFilter(p, capCode)
	if scoped && len(ids) == 0 {
		return nil, false
	}
	return ids, true
}

func (h *Handler) Mount(r chi.Router) {
	r.Route("/ipam", func(r chi.Router) {
		r.Get("/vrfs", h.listVrfs)
		r.Get("/vrfs/{id}", h.getVrf)
		r.Get("/subnets", h.listSubnets)
		r.Get("/subnets/{id}", h.getSubnet)
		r.Get("/subnets/{id}/utilization", h.getSubnetUtilization)
		r.Get("/addresses", h.listAddresses)
		r.Get("/addresses/{id}", h.getAddress)
		r.Get("/fabrics", h.listFabrics)
		r.Get("/fabrics/{id}", h.getFabric)
		r.Get("/supernets", h.listSupernets)
		r.Get("/supernets/{id}", h.getSupernet)
		r.Get("/supernets/{id}/utilization", h.getSupernetUtilization)
		r.Get("/free-space/in-subnets", h.getFreeSpaceInSubnets)
		r.Get("/free-space/prefixes", h.getFreeSpacePrefixes)
		r.Get("/overlays", h.listOverlays)
		r.Get("/vnis", h.listVnis)
		r.Get("/vteps", h.listVteps)
		r.Get("/vtep-memberships", h.listVtepMemberships)
		r.Get("/dhcp/servers", h.listDhcpServers)
		r.Get("/vrf-bgp-peers", h.listVrfBgpPeers)

		// ---- Mutations ----
		// NOTE: PR 42 ports the basic CRUD. CIDR validation, slug
		// regex, supernet-tree containment, per-VRF uniqueness, and
		// fabric's auto-create-default-VRF are deferred to a focused
		// invariants follow-up PR. Simple FK-in-use refusals (delete
		// fabric → vrfs check, delete vrf → supernets, etc.) ARE
		// enforced here as 409s. ABAC fabric-scope enforcement on
		// every mutation that owns or transitively belongs to a fabric
		// landed in PR 54 (1-hop) and PR 55 (2+ hop: subnet, address,
		// vni, vtep, vtep-membership).
		r.With(auth.RequireCapability("ipam:fabrics:create")).Post("/fabrics", h.createFabric)
		r.With(auth.RequireCapability("ipam:fabrics:update")).Patch("/fabrics/{id}", h.updateFabric)
		r.With(auth.RequireCapability("ipam:fabrics:delete")).Delete("/fabrics/{id}", h.deleteFabric)

		r.With(auth.RequireCapability("ipam:vrfs:create")).Post("/vrfs", h.createVrf)
		r.With(auth.RequireCapability("ipam:vrfs:update")).Patch("/vrfs/{id}", h.updateVrf)
		r.With(auth.RequireCapability("ipam:vrfs:delete")).Delete("/vrfs/{id}", h.deleteVrf)

		r.With(auth.RequireCapability("ipam:vrf-bgp-peers:create")).Post("/vrf-bgp-peers", h.createVrfBgpPeer)
		r.With(auth.RequireCapability("ipam:vrf-bgp-peers:update")).Patch("/vrf-bgp-peers/{id}", h.updateVrfBgpPeer)
		r.With(auth.RequireCapability("ipam:vrf-bgp-peers:delete")).Delete("/vrf-bgp-peers/{id}", h.deleteVrfBgpPeer)

		r.With(auth.RequireCapability("ipam:supernets:create")).Post("/supernets", h.createSupernet)
		r.With(auth.RequireCapability("ipam:supernets:update")).Patch("/supernets/{id}", h.updateSupernet)
		r.With(auth.RequireCapability("ipam:supernets:delete")).Delete("/supernets/{id}", h.deleteSupernet)

		r.With(auth.RequireCapability("ipam:subnets:create")).Post("/subnets", h.createSubnet)
		r.With(auth.RequireCapability("ipam:bulk:execute")).Post("/subnets/bulk", h.bulkCreateSubnets)
		r.With(auth.RequireCapability("ipam:subnets:update")).Patch("/subnets/{id}", h.updateSubnet)
		r.With(auth.RequireCapability("ipam:subnets:delete")).Delete("/subnets/{id}", h.deleteSubnet)

		r.With(auth.RequireCapability("ipam:addresses:create")).Post("/addresses", h.createAddress)
		r.With(auth.RequireCapability("ipam:addresses:update")).Patch("/addresses/{id}", h.updateAddress)
		r.With(auth.RequireCapability("ipam:addresses:delete")).Delete("/addresses/{id}", h.deleteAddress)

		r.With(auth.RequireCapability("ipam:overlays:create")).Post("/overlays", h.createOverlay)
		r.With(auth.RequireCapability("ipam:overlays:update")).Patch("/overlays/{id}", h.updateOverlay)
		r.With(auth.RequireCapability("ipam:overlays:delete")).Delete("/overlays/{id}", h.deleteOverlay)

		r.With(auth.RequireCapability("ipam:vnis:create")).Post("/vnis", h.createVni)
		r.With(auth.RequireCapability("ipam:vnis:update")).Patch("/vnis/{id}", h.updateVni)
		r.With(auth.RequireCapability("ipam:vnis:delete")).Delete("/vnis/{id}", h.deleteVni)

		r.With(auth.RequireCapability("ipam:vteps:create")).Post("/vteps", h.createVtep)
		r.With(auth.RequireCapability("ipam:vteps:update")).Patch("/vteps/{id}", h.updateVtep)
		r.With(auth.RequireCapability("ipam:vteps:delete")).Delete("/vteps/{id}", h.deleteVtep)

		r.With(auth.RequireCapability("ipam:vtep-memberships:create")).Post("/vtep-memberships", h.createVtepMembership)
		r.With(auth.RequireCapability("ipam:vtep-memberships:delete")).Delete("/vtep-memberships/{id}", h.deleteVtepMembership)

		r.With(auth.RequireCapability("ipam:dhcp-servers:create")).Post("/dhcp/servers", h.createDhcpServer)
		r.With(auth.RequireCapability("ipam:dhcp-servers:update")).Patch("/dhcp/servers/{id}", h.updateDhcpServer)
		r.With(auth.RequireCapability("ipam:dhcp-servers:delete")).Delete("/dhcp/servers/{id}", h.deleteDhcpServer)
	})
}

type vrfBgpPeersPage struct {
	Items  []dbq.VrfBgpPeer `json:"items"`
	Total  int64            `json:"total"`
	Limit  int32            `json:"limit"`
	Offset int32            `json:"offset"`
}

func (h *Handler) listVrfBgpPeers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(pageSize(q), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
	scopeIds, ok := scopedListFilter(r, "ipam:vrf-bgp-peers:read")
	if !ok {
		httpx.JSON(w, http.StatusOK, vrfBgpPeersPage{Items: nil, Total: 0, Limit: limit, Offset: offset})
		return
	}
	params := dbq.ListVrfBgpPeersParams{
		Limit:          limit,
		Offset:         offset,
		AddressFamily:  strPtr(q.Get("address_family")),
		ScopeFabricIds: scopeIds,
	}
	if v := q.Get("vrf_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "vrf_id is not a uuid")
			return
		}
		params.VrfID = &id
	}
	if v := q.Get("bgp_peer_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "bgp_peer_id is not a uuid")
			return
		}
		params.BgpPeerID = &id
	}
	items, err := h.Q.ListVrfBgpPeers(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountVrfBgpPeers(r.Context(), dbq.CountVrfBgpPeersParams{
		VrfID: params.VrfID, BgpPeerID: params.BgpPeerID, AddressFamily: params.AddressFamily,
		ScopeFabricIds: scopeIds,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, vrfBgpPeersPage{Items: items, Total: total, Limit: limit, Offset: offset})
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
	scopeIds, ok := scopedListFilter(r, "ipam:vrfs:read")
	if !ok {
		httpx.JSON(w, http.StatusOK, vrfsPage{Items: nil, Total: 0, Limit: limit, Offset: offset})
		return
	}
	params := dbq.ListVrfsParams{Limit: limit, Offset: offset, ScopeFabricIds: scopeIds}
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
	total, err := h.Q.CountVrfs(r.Context(), dbq.CountVrfsParams{FabricID: params.FabricID, ScopeFabricIds: scopeIds})
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
	scopeIds, ok := scopedListFilter(r, "ipam:subnets:read")
	if !ok {
		httpx.JSON(w, http.StatusOK, subnetsPage{Items: nil, Total: 0, Limit: limit, Offset: offset})
		return
	}
	params := dbq.ListSubnetsParams{Limit: limit, Offset: offset, Purpose: strPtr(q.Get("purpose")), ScopeFabricIds: scopeIds}
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
		ScopeFabricIds: scopeIds,
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
	scopeIds, ok := scopedListFilter(r, "ipam:addresses:read")
	if !ok {
		httpx.JSON(w, http.StatusOK, addressesPage{Items: nil, Total: 0, Limit: limit, Offset: offset})
		return
	}
	params := dbq.ListIPAddressesParams{
		Limit: limit, Offset: offset,
		Role: strPtr(q.Get("role")), Status: strPtr(q.Get("status")),
		ScopeFabricIds: scopeIds,
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
		ScopeFabricIds: scopeIds,
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
	scopeIds, ok := scopedListFilter(r, "ipam:fabrics:read")
	if !ok {
		httpx.JSON(w, http.StatusOK, fabricsPage{Items: nil, Total: 0, Limit: limit, Offset: offset})
		return
	}
	params := dbq.ListFabricsParams{Limit: limit, Offset: offset, Enclave: strPtr(q.Get("enclave")), ScopeFabricIds: scopeIds}
	items, err := h.Q.ListFabrics(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountFabrics(r.Context(), dbq.CountFabricsParams{Enclave: params.Enclave, ScopeFabricIds: scopeIds})
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
	scopeIds, ok := scopedListFilter(r, "ipam:supernets:read")
	if !ok {
		httpx.JSON(w, http.StatusOK, supernetsPage{Items: nil, Total: 0, Limit: limit, Offset: offset})
		return
	}
	params := dbq.ListSupernetsParams{
		Limit: limit, Offset: offset,
		ParentFilterMode: mode, ParentSupernetID: parentID,
		ScopeFabricIds: scopeIds,
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
		ScopeFabricIds: scopeIds,
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
	scopeIds, ok := scopedListFilter(r, "ipam:overlays:read")
	if !ok {
		httpx.JSON(w, http.StatusOK, overlaysPage{Items: nil, Total: 0, Limit: limit, Offset: offset})
		return
	}
	params := dbq.ListOverlaysParams{Limit: limit, Offset: offset, ScopeFabricIds: scopeIds}
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
	total, err := h.Q.CountOverlays(r.Context(), dbq.CountOverlaysParams{FabricID: params.FabricID, ScopeFabricIds: scopeIds})
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
	scopeIds, ok := scopedListFilter(r, "ipam:vnis:read")
	if !ok {
		httpx.JSON(w, http.StatusOK, vnisPage{Items: nil, Total: 0, Limit: limit, Offset: offset})
		return
	}
	params := dbq.ListVnisParams{Limit: limit, Offset: offset, Kind: strPtr(q.Get("kind")), ScopeFabricIds: scopeIds}
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
		ScopeFabricIds: scopeIds,
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
	scopeIds, ok := scopedListFilter(r, "ipam:vteps:read")
	if !ok {
		httpx.JSON(w, http.StatusOK, vtepsPage{Items: nil, Total: 0, Limit: limit, Offset: offset})
		return
	}
	params := dbq.ListVtepsParams{Limit: limit, Offset: offset, ScopeFabricIds: scopeIds}
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
	total, err := h.Q.CountVteps(r.Context(), dbq.CountVtepsParams{OverlayID: params.OverlayID, AssetID: params.AssetID, ScopeFabricIds: scopeIds})
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
	scopeIds, ok := scopedListFilter(r, "ipam:vtep-memberships:read")
	if !ok {
		httpx.JSON(w, http.StatusOK, membershipsPage{Items: nil, Total: 0, Limit: limit, Offset: offset})
		return
	}
	params := dbq.ListVtepMembershipsParams{Limit: limit, Offset: offset, ScopeFabricIds: scopeIds}
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
		ScopeFabricIds: scopeIds,
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
	scopeIds, ok := scopedListFilter(r, "ipam:dhcp-servers:read")
	if !ok {
		httpx.JSON(w, http.StatusOK, dhcpServersPage{Items: nil, Total: 0, Limit: limit, Offset: offset})
		return
	}
	params := dbq.ListDhcpServersParams{Limit: limit, Offset: offset, ScopeFabricIds: scopeIds}
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
	total, err := h.Q.CountDhcpServers(r.Context(), dbq.CountDhcpServersParams{FabricID: params.FabricID, ScopeFabricIds: scopeIds})
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
