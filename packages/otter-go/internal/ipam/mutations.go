// IPAM mutations (PR 42). Pure pass-through CRUD: CIDR validation,
// containment trees, slug regex, fabric-auto-default-VRF, and per-VRF
// uniqueness are deferred to a focused invariants follow-up. Simple
// FK-in-use refusals (delete fabric → vrfs check, delete vrf →
// supernets check, etc.) ARE enforced as 409s.
package ipam

import (
	"context"
	"encoding/json"
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

// enforceFabric resolves the caller's Principal and refuses with 403 if
// EnforceFabricScope rejects the target fabric. Returns false when a
// response has been written. PR 54 wires this onto every IPAM mutation
// that owns or transitively belongs to a fabric.
func (h *Handler) enforceFabric(w http.ResponseWriter, r *http.Request, fabricID uuid.UUID, capCode string) bool {
	p, _ := auth.From(r.Context())
	if err := auth.EnforceFabricScope(p, fabricID, capCode); err != nil {
		httpx.Error(w, http.StatusForbidden, err.Error())
		return false
	}
	return true
}

// lookupFabricID is a small wrapper that converts the pgx.ErrNoRows
// from a parent-fabric lookup into a 404 with the supplied notFoundMsg.
// Callers should bail (return) when ok=false.
func (h *Handler) lookupFabricID(w http.ResponseWriter, ctx context.Context, fn func(context.Context) (uuid.UUID, error), notFoundMsg string) (uuid.UUID, bool) {
	fid, err := fn(ctx)
	if err != nil {
		mapErr(w, err, notFoundMsg)
		return uuid.Nil, false
	}
	return fid, true
}

// auditMut is a tiny wrapper so the 32 IPAM mutation handlers each
// stay one line of audit instead of four. Use auditMutWithMeta when
// extra context (resolved fabric, conflict count) belongs in metadata.
func (h *Handler) auditMut(r *http.Request, action, targetType, targetID string) {
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: action, TargetType: targetType, TargetID: targetID,
	})
}

// idFromURL returns the {id} path param parsed as a UUID, or writes a
// 400 + returns ok=false on failure.
func idFromURL(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return uuid.Nil, false
	}
	return id, true
}

// mapErr writes an appropriate status from a sqlc/pgx error.
func mapErr(w http.ResponseWriter, err error, notFoundMsg string) {
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, notFoundMsg)
		return
	}
	status, msg := httpx.Mapped(err)
	httpx.Error(w, status, msg)
}

// ---- Fabrics ----

type fabricCreateReq struct {
	Name                  string          `json:"name"`
	Slug                  string          `json:"slug"`
	Description           *string         `json:"description"`
	Enclave               *string         `json:"enclave"`
	Classification        *string         `json:"classification"`
	DnsRecursiveUpstreams json.RawMessage `json:"dns_recursive_upstreams"`
	DnsDenyNetworks       json.RawMessage `json:"dns_deny_networks"`
	CatalogTransferAcl    json.RawMessage `json:"catalog_transfer_acl"`
	RecursiveEngine       string          `json:"recursive_engine"`
}

func (h *Handler) createFabric(w http.ResponseWriter, r *http.Request) {
	var req fabricCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.Slug == "" {
		httpx.Error(w, http.StatusBadRequest, "name and slug required")
		return
	}
	if err := validateSlug(req.Slug); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.RecursiveEngine == "" {
		req.RecursiveEngine = "coredns"
	}
	out, err := h.Q.CreateFabric(r.Context(), dbq.CreateFabricParams{
		Name: req.Name, Slug: req.Slug, Description: req.Description,
		Enclave: req.Enclave, Classification: req.Classification,
		DnsRecursiveUpstreams: req.DnsRecursiveUpstreams,
		DnsDenyNetworks:       req.DnsDenyNetworks,
		CatalogTransferAcl:    req.CatalogTransferAcl,
		RecursiveEngine:       req.RecursiveEngine,
	})
	if err != nil {
		mapErr(w, err, "fabric not found")
		return
	}
	h.auditMut(r, "fabric.create", "fabric", out.ID.String())
	httpx.JSON(w, http.StatusCreated, out)
}

type fabricUpdateReq struct {
	Name                     *string
	Slug                     *string
	Description              *string
	descriptionSet           bool
	Enclave                  *string
	enclaveSet               bool
	Classification           *string
	classificationSet        bool
	DnsRecursiveUpstreams    json.RawMessage
	dnsRecursiveUpstreamsSet bool
	DnsDenyNetworks          json.RawMessage
	dnsDenyNetworksSet       bool
	CatalogTransferAcl       json.RawMessage
	catalogTransferAclSet    bool
	RecursiveEngine          *string
}

func (u *fabricUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["name"]; ok {
		_ = json.Unmarshal(v, &u.Name)
	}
	if v, ok := raw["slug"]; ok {
		_ = json.Unmarshal(v, &u.Slug)
	}
	if v, ok := raw["description"]; ok {
		u.descriptionSet = true
		_ = json.Unmarshal(v, &u.Description)
	}
	if v, ok := raw["enclave"]; ok {
		u.enclaveSet = true
		_ = json.Unmarshal(v, &u.Enclave)
	}
	if v, ok := raw["classification"]; ok {
		u.classificationSet = true
		_ = json.Unmarshal(v, &u.Classification)
	}
	if v, ok := raw["dns_recursive_upstreams"]; ok {
		u.dnsRecursiveUpstreamsSet = true
		u.DnsRecursiveUpstreams = v
	}
	if v, ok := raw["dns_deny_networks"]; ok {
		u.dnsDenyNetworksSet = true
		u.DnsDenyNetworks = v
	}
	if v, ok := raw["catalog_transfer_acl"]; ok {
		u.catalogTransferAclSet = true
		u.CatalogTransferAcl = v
	}
	if v, ok := raw["recursive_engine"]; ok {
		_ = json.Unmarshal(v, &u.RecursiveEngine)
	}
	return nil
}

func (h *Handler) updateFabric(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, id, "ipam:fabrics:update") {
		return
	}
	var req fabricUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	if req.Slug != nil {
		if err := validateSlug(*req.Slug); err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	out, err := h.Q.UpdateFabric(r.Context(), dbq.UpdateFabricParams{
		ID: id, Name: req.Name, Slug: req.Slug,
		DescriptionSet: req.descriptionSet, Description: req.Description,
		EnclaveSet: req.enclaveSet, Enclave: req.Enclave,
		ClassificationSet: req.classificationSet, Classification: req.Classification,
		DnsRecursiveUpstreamsSet: req.dnsRecursiveUpstreamsSet, DnsRecursiveUpstreams: req.DnsRecursiveUpstreams,
		DnsDenyNetworksSet: req.dnsDenyNetworksSet, DnsDenyNetworks: req.DnsDenyNetworks,
		CatalogTransferAclSet: req.catalogTransferAclSet, CatalogTransferAcl: req.CatalogTransferAcl,
		RecursiveEngine: req.RecursiveEngine,
	})
	if err != nil {
		mapErr(w, err, "fabric not found")
		return
	}
	h.auditMut(r, "fabric.update", "fabric", id.String())
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteFabric(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, id, "ipam:fabrics:delete") {
		return
	}
	n, err := h.Q.CountVrfsInFabric(r.Context(), id)
	if err != nil {
		mapErr(w, err, "fabric not found")
		return
	}
	if n > 0 {
		httpx.Error(w, http.StatusConflict, "fabric still has VRFs; remove them first")
		return
	}
	if err := h.Q.DeleteFabric(r.Context(), id); err != nil {
		mapErr(w, err, "fabric not found")
		return
	}
	h.auditMut(r, "fabric.delete", "fabric", id.String())
	w.WriteHeader(http.StatusNoContent)
}

// ---- VRFs ----

type vrfCreateReq struct {
	FabricID    uuid.UUID `json:"fabric_id"`
	Name        string    `json:"name"`
	RouteTarget *string   `json:"route_target"`
	Description *string   `json:"description"`
	IsDefault   bool      `json:"is_default"`
}

func (h *Handler) createVrf(w http.ResponseWriter, r *http.Request) {
	var req vrfCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.FabricID == uuid.Nil {
		httpx.Error(w, http.StatusBadRequest, "fabric_id and name required")
		return
	}
	if !h.enforceFabric(w, r, req.FabricID, "ipam:vrfs:create") {
		return
	}
	out, err := h.Q.CreateVrf(r.Context(), dbq.CreateVrfParams{
		FabricID: req.FabricID, Name: req.Name, RouteTarget: req.RouteTarget,
		Description: req.Description, IsDefault: req.IsDefault,
	})
	if err != nil {
		mapErr(w, err, "vrf not found")
		return
	}
	h.auditMut(r, "vrf.create", "vrf", out.ID.String())
	httpx.JSON(w, http.StatusCreated, out)
}

type vrfUpdateReq struct {
	Name           *string
	RouteTarget    *string
	routeTargetSet bool
	Description    *string
	descriptionSet bool
	IsDefault      *bool
}

func (u *vrfUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["name"]; ok {
		_ = json.Unmarshal(v, &u.Name)
	}
	if v, ok := raw["route_target"]; ok {
		u.routeTargetSet = true
		_ = json.Unmarshal(v, &u.RouteTarget)
	}
	if v, ok := raw["description"]; ok {
		u.descriptionSet = true
		_ = json.Unmarshal(v, &u.Description)
	}
	if v, ok := raw["is_default"]; ok {
		_ = json.Unmarshal(v, &u.IsDefault)
	}
	return nil
}

func (h *Handler) updateVrf(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetVrfFabricID(ctx, id)
	}, "vrf not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "ipam:vrfs:update") {
		return
	}
	var req vrfUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdateVrf(r.Context(), dbq.UpdateVrfParams{
		ID: id, Name: req.Name,
		RouteTargetSet: req.routeTargetSet, RouteTarget: req.RouteTarget,
		DescriptionSet: req.descriptionSet, Description: req.Description,
		IsDefault: req.IsDefault,
	})
	if err != nil {
		mapErr(w, err, "vrf not found")
		return
	}
	h.auditMut(r, "vrf.update", "vrf", id.String())
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteVrf(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetVrfFabricID(ctx, id)
	}, "vrf not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "ipam:vrfs:delete") {
		return
	}
	current, err := h.Q.GetVrf(r.Context(), id)
	if err != nil {
		mapErr(w, err, "vrf not found")
		return
	}
	if current.IsDefault {
		httpx.Error(w, http.StatusBadRequest, "cannot delete a fabric's default VRF")
		return
	}
	n, err := h.Q.CountSupernetsInVrf(r.Context(), id)
	if err != nil {
		mapErr(w, err, "vrf not found")
		return
	}
	if n > 0 {
		httpx.Error(w, http.StatusConflict, "vrf still has supernets; remove them first")
		return
	}
	if err := h.Q.DeleteVrf(r.Context(), id); err != nil {
		mapErr(w, err, "vrf not found")
		return
	}
	h.auditMut(r, "vrf.delete", "vrf", id.String())
	w.WriteHeader(http.StatusNoContent)
}

// ---- VrfBgpPeers ----

type vbpCreateReq struct {
	VrfID         uuid.UUID `json:"vrf_id"`
	BgpPeerID     uuid.UUID `json:"bgp_peer_id"`
	AddressFamily string    `json:"address_family"`
	RD            *string   `json:"rd"`
	Enabled       *bool     `json:"enabled"`
}

func (h *Handler) createVrfBgpPeer(w http.ResponseWriter, r *http.Request) {
	var req vbpCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.VrfID == uuid.Nil || req.BgpPeerID == uuid.Nil || req.AddressFamily == "" {
		httpx.Error(w, http.StatusBadRequest, "vrf_id, bgp_peer_id, address_family required")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	out, err := h.Q.CreateVrfBgpPeer(r.Context(), dbq.CreateVrfBgpPeerParams{
		VrfID: req.VrfID, BgpPeerID: req.BgpPeerID,
		AddressFamily: req.AddressFamily, RD: req.RD, Enabled: enabled,
	})
	if err != nil {
		mapErr(w, err, "binding not found")
		return
	}
	h.auditMut(r, "vrf_bgp_peer.create", "vrf_bgp_peer", out.ID.String())
	httpx.JSON(w, http.StatusCreated, out)
}

type vbpUpdateReq struct {
	RD      *string
	rdSet   bool
	Enabled *bool
}

func (u *vbpUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["rd"]; ok {
		u.rdSet = true
		_ = json.Unmarshal(v, &u.RD)
	}
	if v, ok := raw["enabled"]; ok {
		_ = json.Unmarshal(v, &u.Enabled)
	}
	return nil
}

func (h *Handler) updateVrfBgpPeer(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	var req vbpUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdateVrfBgpPeer(r.Context(), dbq.UpdateVrfBgpPeerParams{
		ID: id, RDSet: req.rdSet, RD: req.RD, Enabled: req.Enabled,
	})
	if err != nil {
		mapErr(w, err, "binding not found")
		return
	}
	h.auditMut(r, "vrf_bgp_peer.update", "vrf_bgp_peer", id.String())
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteVrfBgpPeer(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if err := h.Q.DeleteVrfBgpPeer(r.Context(), id); err != nil {
		mapErr(w, err, "binding not found")
		return
	}
	h.auditMut(r, "vrf_bgp_peer.delete", "vrf_bgp_peer", id.String())
	w.WriteHeader(http.StatusNoContent)
}

// ---- Supernets ----

type supernetCreateReq struct {
	FabricID         uuid.UUID  `json:"fabric_id"`
	VrfID            uuid.UUID  `json:"vrf_id"`
	ParentSupernetID *uuid.UUID `json:"parent_supernet_id"`
	SiteID           *uuid.UUID `json:"site_id"`
	Prefix           string     `json:"prefix"`
	Name             *string    `json:"name"`
	Description      *string    `json:"description"`
	Purpose          *string    `json:"purpose"`
}

func (h *Handler) createSupernet(w http.ResponseWriter, r *http.Request) {
	var req supernetCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.FabricID == uuid.Nil || req.VrfID == uuid.Nil || req.Prefix == "" {
		httpx.Error(w, http.StatusBadRequest, "fabric_id, vrf_id, prefix required")
		return
	}
	if !h.enforceFabric(w, r, req.FabricID, "ipam:supernets:create") {
		return
	}
	prefix, err := parseCIDR(req.Prefix)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	var parentPurpose *string
	if req.ParentSupernetID != nil {
		parent, perr := h.assertSupernetInsideParent(r.Context(), *req.ParentSupernetID, prefix, req.FabricID, req.VrfID)
		if perr != nil {
			httpx.Error(w, http.StatusBadRequest, perr.Error())
			return
		}
		parentPurpose = parent.Purpose
	}
	if perr := validatePurposeCompatible(parentPurpose, req.Purpose); perr != nil {
		httpx.Error(w, http.StatusBadRequest, perr.Error())
		return
	}
	out, err := h.Q.CreateSupernet(r.Context(), dbq.CreateSupernetParams{
		FabricID: req.FabricID, VrfID: req.VrfID,
		ParentSupernetID: req.ParentSupernetID, SiteID: req.SiteID,
		Prefix: req.Prefix, Name: req.Name, Description: req.Description, Purpose: req.Purpose,
	})
	if err != nil {
		mapErr(w, err, "supernet not found")
		return
	}
	h.auditMut(r, "supernet.create", "supernet", out.ID.String())
	httpx.JSON(w, http.StatusCreated, out)
}

type supernetUpdateReq struct {
	ParentSupernetID *uuid.UUID
	parentSet        bool
	SiteID           *uuid.UUID
	siteSet          bool
	Name             *string
	nameSet          bool
	Description      *string
	descriptionSet   bool
	Purpose          *string
	purposeSet       bool
}

func (u *supernetUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["parent_supernet_id"]; ok {
		u.parentSet = true
		_ = json.Unmarshal(v, &u.ParentSupernetID)
	}
	if v, ok := raw["site_id"]; ok {
		u.siteSet = true
		_ = json.Unmarshal(v, &u.SiteID)
	}
	if v, ok := raw["name"]; ok {
		u.nameSet = true
		_ = json.Unmarshal(v, &u.Name)
	}
	if v, ok := raw["description"]; ok {
		u.descriptionSet = true
		_ = json.Unmarshal(v, &u.Description)
	}
	if v, ok := raw["purpose"]; ok {
		u.purposeSet = true
		_ = json.Unmarshal(v, &u.Purpose)
	}
	return nil
}

func (h *Handler) updateSupernet(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	parent, err := h.Q.GetSupernetVrfAndFabric(r.Context(), id)
	if err != nil {
		mapErr(w, err, "supernet not found")
		return
	}
	if !h.enforceFabric(w, r, parent.FabricID, "ipam:supernets:update") {
		return
	}
	var req supernetUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdateSupernet(r.Context(), dbq.UpdateSupernetParams{
		ID: id,
		ParentSet: req.parentSet, ParentSupernetID: req.ParentSupernetID,
		SiteSet: req.siteSet, SiteID: req.SiteID,
		NameSet: req.nameSet, Name: req.Name,
		DescriptionSet: req.descriptionSet, Description: req.Description,
		PurposeSet: req.purposeSet, Purpose: req.Purpose,
	})
	if err != nil {
		mapErr(w, err, "supernet not found")
		return
	}
	h.auditMut(r, "supernet.update", "supernet", id.String())
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteSupernet(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	parent, err := h.Q.GetSupernetVrfAndFabric(r.Context(), id)
	if err != nil {
		mapErr(w, err, "supernet not found")
		return
	}
	if !h.enforceFabric(w, r, parent.FabricID, "ipam:supernets:delete") {
		return
	}
	n, err := h.Q.CountSubnetsInSupernet(r.Context(), id)
	if err != nil {
		mapErr(w, err, "supernet not found")
		return
	}
	if n > 0 {
		httpx.Error(w, http.StatusConflict, "supernet still has subnets; remove them first")
		return
	}
	if err := h.Q.DeleteSupernet(r.Context(), id); err != nil {
		mapErr(w, err, "supernet not found")
		return
	}
	h.auditMut(r, "supernet.delete", "supernet", id.String())
	w.WriteHeader(http.StatusNoContent)
}

// ---- Subnets ----

type subnetCreateReq struct {
	SupernetID  uuid.UUID  `json:"supernet_id"`
	SiteID      *uuid.UUID `json:"site_id"`
	VniID       *uuid.UUID `json:"vni_id"`
	Prefix      string     `json:"prefix"`
	Name        *string    `json:"name"`
	Description *string    `json:"description"`
	Purpose     *string    `json:"purpose"`
	VlanID      *int32     `json:"vlan_id"`
	Gateway     *string    `json:"gateway"`
}

func (h *Handler) createSubnet(w http.ResponseWriter, r *http.Request) {
	var req subnetCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.SupernetID == uuid.Nil || req.Prefix == "" {
		httpx.Error(w, http.StatusBadRequest, "supernet_id and prefix required")
		return
	}
	prefix, err := parseCIDR(req.Prefix)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	supernet, err := h.assertSubnetInsideSupernet(r.Context(), req.SupernetID, prefix)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if perr := validatePurposeCompatible(supernet.Purpose, req.Purpose); perr != nil {
		httpx.Error(w, http.StatusBadRequest, perr.Error())
		return
	}
	parent, err := h.Q.GetSupernetVrfAndFabric(r.Context(), req.SupernetID)
	if err != nil {
		mapErr(w, err, "supernet not found")
		return
	}
	out, err := h.Q.CreateSubnet(r.Context(), dbq.CreateSubnetParams{
		SupernetID: req.SupernetID, FabricID: parent.FabricID, VrfID: parent.VrfID,
		SiteID: req.SiteID, VniID: req.VniID, Prefix: req.Prefix,
		Name: req.Name, Description: req.Description, Purpose: req.Purpose,
		VlanID: req.VlanID, Gateway: req.Gateway,
	})
	if err != nil {
		mapErr(w, err, "subnet not found")
		return
	}
	h.auditMut(r, "subnet.create", "subnet", out.ID.String())
	httpx.JSON(w, http.StatusCreated, out)
}

type subnetUpdateReq struct {
	SupernetID     *uuid.UUID
	SiteID         *uuid.UUID
	siteSet        bool
	VniID          *uuid.UUID
	vniSet         bool
	Name           *string
	nameSet        bool
	Description    *string
	descriptionSet bool
	Purpose        *string
	purposeSet     bool
	VlanID         *int32
	vlanSet        bool
	Gateway        *string
	gatewaySet     bool
}

func (u *subnetUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["supernet_id"]; ok {
		_ = json.Unmarshal(v, &u.SupernetID)
	}
	if v, ok := raw["site_id"]; ok {
		u.siteSet = true
		_ = json.Unmarshal(v, &u.SiteID)
	}
	if v, ok := raw["vni_id"]; ok {
		u.vniSet = true
		_ = json.Unmarshal(v, &u.VniID)
	}
	if v, ok := raw["name"]; ok {
		u.nameSet = true
		_ = json.Unmarshal(v, &u.Name)
	}
	if v, ok := raw["description"]; ok {
		u.descriptionSet = true
		_ = json.Unmarshal(v, &u.Description)
	}
	if v, ok := raw["purpose"]; ok {
		u.purposeSet = true
		_ = json.Unmarshal(v, &u.Purpose)
	}
	if v, ok := raw["vlan_id"]; ok {
		u.vlanSet = true
		_ = json.Unmarshal(v, &u.VlanID)
	}
	if v, ok := raw["gateway"]; ok {
		u.gatewaySet = true
		_ = json.Unmarshal(v, &u.Gateway)
	}
	return nil
}

func (h *Handler) updateSubnet(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	var req subnetUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdateSubnet(r.Context(), dbq.UpdateSubnetParams{
		ID: id, SupernetID: req.SupernetID,
		SiteSet: req.siteSet, SiteID: req.SiteID,
		VniSet: req.vniSet, VniID: req.VniID,
		NameSet: req.nameSet, Name: req.Name,
		DescriptionSet: req.descriptionSet, Description: req.Description,
		PurposeSet: req.purposeSet, Purpose: req.Purpose,
		VlanSet: req.vlanSet, VlanID: req.VlanID,
		GatewaySet: req.gatewaySet, Gateway: req.Gateway,
	})
	if err != nil {
		mapErr(w, err, "subnet not found")
		return
	}
	h.auditMut(r, "subnet.update", "subnet", id.String())
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteSubnet(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	n, err := h.Q.CountAddressesInSubnet(r.Context(), id)
	if err != nil {
		mapErr(w, err, "subnet not found")
		return
	}
	if n > 0 {
		httpx.Error(w, http.StatusConflict, "subnet still has addresses; remove them first")
		return
	}
	if err := h.Q.DeleteSubnet(r.Context(), id); err != nil {
		mapErr(w, err, "subnet not found")
		return
	}
	h.auditMut(r, "subnet.delete", "subnet", id.String())
	w.WriteHeader(http.StatusNoContent)
}

// ---- IPAddresses ----

type addressCreateReq struct {
	SubnetID    uuid.UUID  `json:"subnet_id"`
	AssetID     *uuid.UUID `json:"asset_id"`
	Address     string     `json:"address"`
	Role        string     `json:"role"`
	Status      string     `json:"status"`
	Source      string     `json:"source"`
	DnsName     *string    `json:"dns_name"`
	Description *string    `json:"description"`
}

func (h *Handler) createAddress(w http.ResponseWriter, r *http.Request) {
	var req addressCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.SubnetID == uuid.Nil || req.Address == "" {
		httpx.Error(w, http.StatusBadRequest, "subnet_id and address required")
		return
	}
	addr, err := parseAddr(req.Address)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, aerr := h.assertAddressInSubnet(r.Context(), req.SubnetID, addr); aerr != nil {
		httpx.Error(w, http.StatusBadRequest, aerr.Error())
		return
	}
	if req.Role == "" {
		req.Role = "data"
	}
	if req.Status == "" {
		req.Status = "active"
	}
	if req.Source == "" {
		req.Source = "static"
	}
	out, err := h.Q.CreateIPAddress(r.Context(), dbq.CreateIPAddressParams{
		SubnetID: req.SubnetID, AssetID: req.AssetID, Address: req.Address,
		Role: req.Role, Status: req.Status, Source: req.Source,
		DnsName: req.DnsName, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "address not found")
		return
	}
	h.auditMut(r, "ip_address.create", "ip_address", out.ID.String())
	httpx.JSON(w, http.StatusCreated, out)
}

type addressUpdateReq struct {
	AssetID        *uuid.UUID
	assetSet       bool
	Role           *string
	Status         *string
	DnsName        *string
	dnsSet         bool
	Description    *string
	descriptionSet bool
}

func (u *addressUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["asset_id"]; ok {
		u.assetSet = true
		_ = json.Unmarshal(v, &u.AssetID)
	}
	if v, ok := raw["role"]; ok {
		_ = json.Unmarshal(v, &u.Role)
	}
	if v, ok := raw["status"]; ok {
		_ = json.Unmarshal(v, &u.Status)
	}
	if v, ok := raw["dns_name"]; ok {
		u.dnsSet = true
		_ = json.Unmarshal(v, &u.DnsName)
	}
	if v, ok := raw["description"]; ok {
		u.descriptionSet = true
		_ = json.Unmarshal(v, &u.Description)
	}
	return nil
}

func (h *Handler) updateAddress(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	var req addressUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdateIPAddress(r.Context(), dbq.UpdateIPAddressParams{
		ID: id, AssetSet: req.assetSet, AssetID: req.AssetID,
		Role: req.Role, Status: req.Status,
		DnsSet: req.dnsSet, DnsName: req.DnsName,
		DescriptionSet: req.descriptionSet, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "address not found")
		return
	}
	h.auditMut(r, "ip_address.update", "ip_address", id.String())
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteAddress(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if err := h.Q.DeleteIPAddress(r.Context(), id); err != nil {
		mapErr(w, err, "address not found")
		return
	}
	h.auditMut(r, "ip_address.delete", "ip_address", id.String())
	w.WriteHeader(http.StatusNoContent)
}

// ---- Overlays ----

type overlayCreateReq struct {
	FabricID      uuid.UUID  `json:"fabric_id"`
	Name          string     `json:"name"`
	Kind          string     `json:"kind"`
	UDPPort       *int32     `json:"udp_port"`
	MTU           *int32     `json:"mtu"`
	UnderlayVrfID *uuid.UUID `json:"underlay_vrf_id"`
	Description   *string    `json:"description"`
}

func (h *Handler) createOverlay(w http.ResponseWriter, r *http.Request) {
	var req overlayCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.FabricID == uuid.Nil || req.Name == "" {
		httpx.Error(w, http.StatusBadRequest, "fabric_id and name required")
		return
	}
	if !h.enforceFabric(w, r, req.FabricID, "ipam:overlays:create") {
		return
	}
	if req.Kind == "" {
		req.Kind = "vxlan"
	}
	udp := int32(4789)
	if req.UDPPort != nil {
		udp = *req.UDPPort
	}
	out, err := h.Q.CreateOverlay(r.Context(), dbq.CreateOverlayParams{
		FabricID: req.FabricID, Name: req.Name, Kind: req.Kind, UDPPort: udp,
		MTU: req.MTU, UnderlayVrfID: req.UnderlayVrfID, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "overlay not found")
		return
	}
	h.auditMut(r, "overlay.create", "overlay", out.ID.String())
	httpx.JSON(w, http.StatusCreated, out)
}

type overlayUpdateReq struct {
	Name           *string
	Kind           *string
	UDPPort        *int32
	MTU            *int32
	mtuSet         bool
	UnderlayVrfID  *uuid.UUID
	underlaySet    bool
	Description    *string
	descriptionSet bool
}

func (u *overlayUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["name"]; ok {
		_ = json.Unmarshal(v, &u.Name)
	}
	if v, ok := raw["kind"]; ok {
		_ = json.Unmarshal(v, &u.Kind)
	}
	if v, ok := raw["udp_port"]; ok {
		_ = json.Unmarshal(v, &u.UDPPort)
	}
	if v, ok := raw["mtu"]; ok {
		u.mtuSet = true
		_ = json.Unmarshal(v, &u.MTU)
	}
	if v, ok := raw["underlay_vrf_id"]; ok {
		u.underlaySet = true
		_ = json.Unmarshal(v, &u.UnderlayVrfID)
	}
	if v, ok := raw["description"]; ok {
		u.descriptionSet = true
		_ = json.Unmarshal(v, &u.Description)
	}
	return nil
}

func (h *Handler) updateOverlay(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetOverlayFabricID(ctx, id)
	}, "overlay not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "ipam:overlays:update") {
		return
	}
	var req overlayUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdateOverlay(r.Context(), dbq.UpdateOverlayParams{
		ID: id, Name: req.Name, Kind: req.Kind, UDPPort: req.UDPPort,
		MTUSet: req.mtuSet, MTU: req.MTU,
		UnderlaySet: req.underlaySet, UnderlayVrfID: req.UnderlayVrfID,
		DescriptionSet: req.descriptionSet, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "overlay not found")
		return
	}
	h.auditMut(r, "overlay.update", "overlay", id.String())
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteOverlay(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetOverlayFabricID(ctx, id)
	}, "overlay not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "ipam:overlays:delete") {
		return
	}
	n, err := h.Q.CountVnisInOverlay(r.Context(), id)
	if err != nil {
		mapErr(w, err, "overlay not found")
		return
	}
	if n > 0 {
		httpx.Error(w, http.StatusConflict, "overlay still has VNIs; remove them first")
		return
	}
	if err := h.Q.DeleteOverlay(r.Context(), id); err != nil {
		mapErr(w, err, "overlay not found")
		return
	}
	h.auditMut(r, "overlay.delete", "overlay", id.String())
	w.WriteHeader(http.StatusNoContent)
}

// ---- VNIs ----

type vniCreateReq struct {
	OverlayID       uuid.UUID  `json:"overlay_id"`
	VNI             int32      `json:"vni"`
	Kind            string     `json:"kind"`
	Name            *string    `json:"name"`
	Description     *string    `json:"description"`
	VlanID          *int32     `json:"vlan_id"`
	EvpnRouteTarget *string    `json:"evpn_route_target"`
	VrfID           *uuid.UUID `json:"vrf_id"`
}

func (h *Handler) createVni(w http.ResponseWriter, r *http.Request) {
	var req vniCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.OverlayID == uuid.Nil || req.VNI == 0 {
		httpx.Error(w, http.StatusBadRequest, "overlay_id and vni required")
		return
	}
	if req.Kind == "" {
		req.Kind = "l2"
	}
	if err := validateVni(req.VNI); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateVniKind(req.Kind, req.VlanID, req.VrfID); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := h.Q.CreateVni(r.Context(), dbq.CreateVniParams{
		OverlayID: req.OverlayID, VNI: req.VNI, Kind: req.Kind,
		Name: req.Name, Description: req.Description, VlanID: req.VlanID,
		EvpnRouteTarget: req.EvpnRouteTarget, VrfID: req.VrfID,
	})
	if err != nil {
		mapErr(w, err, "vni not found")
		return
	}
	h.auditMut(r, "vni.create", "vni", out.ID.String())
	httpx.JSON(w, http.StatusCreated, out)
}

type vniUpdateReq struct {
	Name            *string
	nameSet         bool
	Description     *string
	descriptionSet  bool
	VlanID          *int32
	vlanSet         bool
	EvpnRouteTarget *string
	rtSet           bool
	Kind            *string
	VrfID           *uuid.UUID
	vrfSet          bool
}

func (u *vniUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["name"]; ok {
		u.nameSet = true
		_ = json.Unmarshal(v, &u.Name)
	}
	if v, ok := raw["description"]; ok {
		u.descriptionSet = true
		_ = json.Unmarshal(v, &u.Description)
	}
	if v, ok := raw["vlan_id"]; ok {
		u.vlanSet = true
		_ = json.Unmarshal(v, &u.VlanID)
	}
	if v, ok := raw["evpn_route_target"]; ok {
		u.rtSet = true
		_ = json.Unmarshal(v, &u.EvpnRouteTarget)
	}
	if v, ok := raw["kind"]; ok {
		_ = json.Unmarshal(v, &u.Kind)
	}
	if v, ok := raw["vrf_id"]; ok {
		u.vrfSet = true
		_ = json.Unmarshal(v, &u.VrfID)
	}
	return nil
}

func (h *Handler) updateVni(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	var req vniUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdateVni(r.Context(), dbq.UpdateVniParams{
		ID: id,
		NameSet: req.nameSet, Name: req.Name,
		DescriptionSet: req.descriptionSet, Description: req.Description,
		VlanSet: req.vlanSet, VlanID: req.VlanID,
		RTSet: req.rtSet, EvpnRouteTarget: req.EvpnRouteTarget,
		Kind: req.Kind,
		VrfSet: req.vrfSet, VrfID: req.VrfID,
	})
	if err != nil {
		mapErr(w, err, "vni not found")
		return
	}
	h.auditMut(r, "vni.update", "vni", id.String())
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteVni(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if err := h.Q.DeleteVni(r.Context(), id); err != nil {
		mapErr(w, err, "vni not found")
		return
	}
	h.auditMut(r, "vni.delete", "vni", id.String())
	w.WriteHeader(http.StatusNoContent)
}

// ---- VTEPs ----

type vtepCreateReq struct {
	OverlayID   uuid.UUID `json:"overlay_id"`
	AssetID     uuid.UUID `json:"asset_id"`
	LoopbackIP  *string   `json:"loopback_ip"`
	Role        string    `json:"role"`
	Description *string   `json:"description"`
}

func (h *Handler) createVtep(w http.ResponseWriter, r *http.Request) {
	var req vtepCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.OverlayID == uuid.Nil || req.AssetID == uuid.Nil {
		httpx.Error(w, http.StatusBadRequest, "overlay_id and asset_id required")
		return
	}
	if req.Role == "" {
		req.Role = "leaf"
	}
	out, err := h.Q.CreateVtep(r.Context(), dbq.CreateVtepParams{
		OverlayID: req.OverlayID, AssetID: req.AssetID,
		LoopbackIP: req.LoopbackIP, Role: req.Role, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "vtep not found")
		return
	}
	h.auditMut(r, "vtep.create", "vtep", out.ID.String())
	httpx.JSON(w, http.StatusCreated, out)
}

type vtepUpdateReq struct {
	LoopbackIP     *string
	loopbackSet    bool
	Role           *string
	Description    *string
	descriptionSet bool
}

func (u *vtepUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["loopback_ip"]; ok {
		u.loopbackSet = true
		_ = json.Unmarshal(v, &u.LoopbackIP)
	}
	if v, ok := raw["role"]; ok {
		_ = json.Unmarshal(v, &u.Role)
	}
	if v, ok := raw["description"]; ok {
		u.descriptionSet = true
		_ = json.Unmarshal(v, &u.Description)
	}
	return nil
}

func (h *Handler) updateVtep(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	var req vtepUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdateVtep(r.Context(), dbq.UpdateVtepParams{
		ID: id, LoopbackSet: req.loopbackSet, LoopbackIP: req.LoopbackIP,
		Role: req.Role,
		DescriptionSet: req.descriptionSet, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "vtep not found")
		return
	}
	h.auditMut(r, "vtep.update", "vtep", id.String())
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteVtep(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if err := h.Q.DeleteVtep(r.Context(), id); err != nil {
		mapErr(w, err, "vtep not found")
		return
	}
	h.auditMut(r, "vtep.delete", "vtep", id.String())
	w.WriteHeader(http.StatusNoContent)
}

// ---- VTEP/VNI memberships ----

type membershipCreateReq struct {
	VtepID uuid.UUID `json:"vtep_id"`
	VniID  uuid.UUID `json:"vni_id"`
}

func (h *Handler) createVtepMembership(w http.ResponseWriter, r *http.Request) {
	var req membershipCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.VtepID == uuid.Nil || req.VniID == uuid.Nil {
		httpx.Error(w, http.StatusBadRequest, "vtep_id and vni_id required")
		return
	}
	out, err := h.Q.CreateVtepMembership(r.Context(), dbq.CreateVtepMembershipParams{
		VtepID: req.VtepID, VniID: req.VniID,
	})
	if err != nil {
		mapErr(w, err, "membership not found")
		return
	}
	h.auditMut(r, "vtep_membership.create", "vtep_vni_membership", out.ID.String())
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *Handler) deleteVtepMembership(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if err := h.Q.DeleteVtepMembership(r.Context(), id); err != nil {
		mapErr(w, err, "membership not found")
		return
	}
	h.auditMut(r, "vtep_membership.delete", "vtep_vni_membership", id.String())
	w.WriteHeader(http.StatusNoContent)
}

// ---- DHCP servers ----

type dhcpCreateReq struct {
	Name         string    `json:"name"`
	FabricID     uuid.UUID `json:"fabric_id"`
	KeaURL       string    `json:"kea_url"`
	AuthUsername *string   `json:"auth_username"`
	AuthPassword *string   `json:"auth_password"`
	Enabled      *bool     `json:"enabled"`
}

func (h *Handler) createDhcpServer(w http.ResponseWriter, r *http.Request) {
	var req dhcpCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.Name == "" || req.FabricID == uuid.Nil || req.KeaURL == "" {
		httpx.Error(w, http.StatusBadRequest, "name, fabric_id, kea_url required")
		return
	}
	if !h.enforceFabric(w, r, req.FabricID, "ipam:dhcp-servers:create") {
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	out, err := h.Q.CreateDhcpServer(r.Context(), dbq.CreateDhcpServerParams{
		Name: req.Name, FabricID: req.FabricID, KeaURL: req.KeaURL,
		AuthUsername: req.AuthUsername, AuthPassword: req.AuthPassword, Enabled: enabled,
	})
	if err != nil {
		mapErr(w, err, "dhcp server not found")
		return
	}
	h.auditMut(r, "dhcp_server.create", "dhcp_server", out.ID.String())
	httpx.JSON(w, http.StatusCreated, out)
}

type dhcpUpdateReq struct {
	Name         *string
	KeaURL       *string
	AuthUsername *string
	usernameSet  bool
	AuthPassword *string
	passwordSet  bool
	Enabled      *bool
}

func (u *dhcpUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["name"]; ok {
		_ = json.Unmarshal(v, &u.Name)
	}
	if v, ok := raw["kea_url"]; ok {
		_ = json.Unmarshal(v, &u.KeaURL)
	}
	if v, ok := raw["auth_username"]; ok {
		u.usernameSet = true
		_ = json.Unmarshal(v, &u.AuthUsername)
	}
	if v, ok := raw["auth_password"]; ok {
		u.passwordSet = true
		_ = json.Unmarshal(v, &u.AuthPassword)
	}
	if v, ok := raw["enabled"]; ok {
		_ = json.Unmarshal(v, &u.Enabled)
	}
	return nil
}

func (h *Handler) updateDhcpServer(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetDhcpServerFabricID(ctx, id)
	}, "dhcp server not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "ipam:dhcp-servers:update") {
		return
	}
	var req dhcpUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdateDhcpServer(r.Context(), dbq.UpdateDhcpServerParams{
		ID: id, Name: req.Name, KeaURL: req.KeaURL,
		UsernameSet: req.usernameSet, AuthUsername: req.AuthUsername,
		PasswordSet: req.passwordSet, AuthPassword: req.AuthPassword,
		Enabled: req.Enabled,
	})
	if err != nil {
		mapErr(w, err, "dhcp server not found")
		return
	}
	h.auditMut(r, "dhcp_server.update", "dhcp_server", id.String())
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteDhcpServer(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetDhcpServerFabricID(ctx, id)
	}, "dhcp server not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "ipam:dhcp-servers:delete") {
		return
	}
	if err := h.Q.DeleteDhcpServer(r.Context(), id); err != nil {
		mapErr(w, err, "dhcp server not found")
		return
	}
	h.auditMut(r, "dhcp_server.delete", "dhcp_server", id.String())
	w.WriteHeader(http.StatusNoContent)
}
