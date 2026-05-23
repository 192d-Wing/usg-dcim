// DNS mutations (PR 43). Basic CRUD only. Action endpoints
// (freeze/unfreeze, import, sync-from-ipam, enable/disable-dnssec,
// nsec3, render-status, health-checks/result, blocklists entries/bulk)
// are deferred to a focused follow-up.
//
// ABAC enforcement:
//   - PR 57 — fabric-rooted mutations: zones, records (2-hop via
//     zone), servers, anycast-groups, forwarders, catalog-zones,
//     blocklists, blocklist-entries (2-hop via blocklist), views,
//     health-checks.
//   - PR 58 — bgp_peers via EnforceSiteScope (site_id, with
//     region + site-group expansion through the shared siteScopeQ
//     interface); anycast_bgp_bindings via EnforceFabricScope on the
//     dns_server's fabric (the binding's fabric-side anchor).
package dns

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
// response has been written. PR 57 wires this onto every DNS mutation
// that owns or transitively belongs to a fabric.
func (h *Handler) enforceFabric(w http.ResponseWriter, r *http.Request, fabricID uuid.UUID, capCode string) bool {
	p, _ := auth.From(r.Context())
	if err := auth.EnforceFabricScope(p, fabricID, capCode); err != nil {
		httpx.Error(w, http.StatusForbidden, err.Error())
		return false
	}
	return true
}

// enforceSite is the site-scoped twin of enforceFabric (PR 58). Used by
// bgp_peers mutations, which carry site_id rather than fabric_id.
// EnforceSiteScope is DB-backed (region + site-group expansion) so it
// needs the Handler.Q's siteScopeQ methods to be wired through.
func (h *Handler) enforceSite(w http.ResponseWriter, r *http.Request, siteID uuid.UUID, capCode string) bool {
	p, _ := auth.From(r.Context())
	if err := auth.EnforceSiteScope(r.Context(), h.Q, p, siteID, capCode); err != nil {
		httpx.Error(w, http.StatusForbidden, err.Error())
		return false
	}
	return true
}

// lookupFabricID runs a slim parent-fabric lookup and converts
// pgx.ErrNoRows into a 404 with the supplied notFoundMsg. Returns
// ok=false when a response has been written.
func (h *Handler) lookupFabricID(w http.ResponseWriter, ctx context.Context, fn func(context.Context) (uuid.UUID, error), notFoundMsg string) (uuid.UUID, bool) {
	fid, err := fn(ctx)
	if err != nil {
		mapErr(w, err, notFoundMsg)
		return uuid.Nil, false
	}
	return fid, true
}

func idFromURL(w http.ResponseWriter, r *http.Request, key string) (uuid.UUID, bool) {
	if key == "" {
		key = "id"
	}
	id, err := uuid.Parse(chi.URLParam(r, key))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, key+" is not a uuid")
		return uuid.Nil, false
	}
	return id, true
}

func mapErr(w http.ResponseWriter, err error, notFoundMsg string) {
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, notFoundMsg)
		return
	}
	status, msg := httpx.Mapped(err)
	httpx.Error(w, status, msg)
}

// ---- DNS Zones ----

type zoneCreateReq struct {
	Name            string     `json:"name"`
	Kind            string     `json:"kind"`
	FabricID        uuid.UUID  `json:"fabric_id"`
	SiteID          *uuid.UUID `json:"site_id"`
	Description     *string    `json:"description"`
	SoaMname        string     `json:"soa_mname"`
	SoaRname        string     `json:"soa_rname"`
	SoaRefresh      *int32     `json:"soa_refresh"`
	SoaRetry        *int32     `json:"soa_retry"`
	SoaExpire       *int32     `json:"soa_expire"`
	SoaMinimum      *int32     `json:"soa_minimum"`
	DefaultTTL      *int32     `json:"default_ttl"`
	ZskRotationDays *int32     `json:"zsk_rotation_days"`
	PublishCds      *bool      `json:"publish_cds"`
}

func (h *Handler) createZone(w http.ResponseWriter, r *http.Request) {
	var req zoneCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.Name == "" || req.Kind == "" || req.FabricID == uuid.Nil {
		httpx.Error(w, http.StatusBadRequest, "name, kind, fabric_id required")
		return
	}
	if !h.enforceFabric(w, r, req.FabricID, "dns:zones:create") {
		return
	}
	if req.SoaMname == "" {
		req.SoaMname = "ns1"
	}
	if req.SoaRname == "" {
		req.SoaRname = "hostmaster"
	}
	def := func(p *int32, d int32) int32 {
		if p != nil {
			return *p
		}
		return d
	}
	publishCds := true
	if req.PublishCds != nil {
		publishCds = *req.PublishCds
	}
	out, err := h.Q.CreateDnsZone(r.Context(), dbq.CreateDnsZoneParams{
		Name: req.Name, Kind: req.Kind, FabricID: req.FabricID, SiteID: req.SiteID,
		Description: req.Description,
		SoaMname:    req.SoaMname, SoaRname: req.SoaRname,
		SoaRefresh:  def(req.SoaRefresh, 900),
		SoaRetry:    def(req.SoaRetry, 900),
		SoaExpire:   def(req.SoaExpire, 1800),
		SoaMinimum:  def(req.SoaMinimum, 60),
		DefaultTTL:  def(req.DefaultTTL, 60),
		ZskRotationDays: def(req.ZskRotationDays, 0),
		PublishCds:  publishCds,
	})
	if err != nil {
		mapErr(w, err, "zone not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "dns_zone.create", TargetType: "dns_zone", TargetID: out.ID.String()})
	httpx.JSON(w, http.StatusCreated, out)
}

type zoneUpdateReq struct {
	Description     *string
	descriptionSet  bool
	ZskRotationDays *int32
	SoaMname        *string
	SoaRname        *string
	SoaRefresh      *int32
	SoaRetry        *int32
	SoaExpire       *int32
	SoaMinimum      *int32
	DefaultTTL      *int32
	PublishCds      *bool
}

func (u *zoneUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["description"]; ok {
		u.descriptionSet = true
		_ = json.Unmarshal(v, &u.Description)
	}
	for k, dst := range map[string]any{
		"zsk_rotation_days": &u.ZskRotationDays, "soa_mname": &u.SoaMname,
		"soa_rname": &u.SoaRname, "soa_refresh": &u.SoaRefresh,
		"soa_retry": &u.SoaRetry, "soa_expire": &u.SoaExpire,
		"soa_minimum": &u.SoaMinimum, "default_ttl": &u.DefaultTTL,
		"publish_cds": &u.PublishCds,
	} {
		if v, ok := raw[k]; ok {
			_ = json.Unmarshal(v, dst)
		}
	}
	return nil
}

func (h *Handler) updateZone(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetDnsZoneFabricID(ctx, id)
	}, "zone not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "dns:zones:update") {
		return
	}
	var req zoneUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdateDnsZone(r.Context(), dbq.UpdateDnsZoneParams{
		ID: id,
		DescriptionSet: req.descriptionSet, Description: req.Description,
		ZskRotationDays: req.ZskRotationDays,
		SoaMname: req.SoaMname, SoaRname: req.SoaRname,
		SoaRefresh: req.SoaRefresh, SoaRetry: req.SoaRetry,
		SoaExpire: req.SoaExpire, SoaMinimum: req.SoaMinimum,
		DefaultTTL: req.DefaultTTL, PublishCds: req.PublishCds,
	})
	if err != nil {
		mapErr(w, err, "zone not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "dns_zone.update", TargetType: "dns_zone", TargetID: id.String()})
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteZone(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetDnsZoneFabricID(ctx, id)
	}, "zone not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "dns:zones:delete") {
		return
	}
	if err := h.Q.DeleteDnsZone(r.Context(), id); err != nil {
		mapErr(w, err, "zone not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "dns_zone.delete", TargetType: "dns_zone", TargetID: id.String()})
	w.WriteHeader(http.StatusNoContent)
}

// ---- DNS Records ----

type recordCreateReq struct {
	ZoneID        uuid.UUID       `json:"zone_id"`
	Name          string          `json:"name"`
	Type          string          `json:"type"`
	TTL           *int32          `json:"ttl"`
	Data          json.RawMessage `json:"data"`
	ViewID        *uuid.UUID      `json:"view_id"`
	HealthCheckID *uuid.UUID      `json:"health_check_id"`
	Description   *string         `json:"description"`
}

func (h *Handler) createRecord(w http.ResponseWriter, r *http.Request) {
	var req recordCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.ZoneID == uuid.Nil || req.Name == "" || req.Type == "" || len(req.Data) == 0 {
		httpx.Error(w, http.StatusBadRequest, "zone_id, name, type, data required")
		return
	}
	zone, err := h.Q.GetDnsZone(r.Context(), req.ZoneID)
	if err != nil {
		mapErr(w, err, "zone not found")
		return
	}
	if !h.enforceFabric(w, r, zone.FabricID, "dns:records:create") {
		return
	}
	if zone.Frozen {
		httpx.Error(w, http.StatusUnprocessableEntity, errZoneFrozen.Error())
		return
	}
	if err := validateRecordData(req.Type, req.Data); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := h.Q.CreateDnsRecord(r.Context(), dbq.CreateDnsRecordParams{
		ZoneID: req.ZoneID, Name: req.Name, Type: req.Type, TTL: req.TTL,
		Data: req.Data, ViewID: req.ViewID, HealthCheckID: req.HealthCheckID,
		Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "record not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "dns_record.create", TargetType: "dns_record", TargetID: out.ID.String()})
	httpx.JSON(w, http.StatusCreated, out)
}

type recordUpdateReq struct {
	Name           *string
	TTL            *int32
	ttlSet         bool
	Data           json.RawMessage
	ViewID         *uuid.UUID
	viewSet        bool
	HealthCheckID  *uuid.UUID
	hcSet          bool
	Description    *string
	descriptionSet bool
}

func (u *recordUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["name"]; ok {
		_ = json.Unmarshal(v, &u.Name)
	}
	if v, ok := raw["ttl"]; ok {
		u.ttlSet = true
		_ = json.Unmarshal(v, &u.TTL)
	}
	if v, ok := raw["data"]; ok {
		u.Data = v
	}
	if v, ok := raw["view_id"]; ok {
		u.viewSet = true
		_ = json.Unmarshal(v, &u.ViewID)
	}
	if v, ok := raw["health_check_id"]; ok {
		u.hcSet = true
		_ = json.Unmarshal(v, &u.HealthCheckID)
	}
	if v, ok := raw["description"]; ok {
		u.descriptionSet = true
		_ = json.Unmarshal(v, &u.Description)
	}
	return nil
}

func (h *Handler) updateRecord(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetDnsRecordFabricID(ctx, id)
	}, "record not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "dns:records:update") {
		return
	}
	var req recordUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	zf, err := h.Q.GetZoneFrozenByRecord(r.Context(), id)
	if err != nil {
		mapErr(w, err, "record not found")
		return
	}
	if zf.Frozen {
		httpx.Error(w, http.StatusUnprocessableEntity, errZoneFrozen.Error())
		return
	}
	out, err := h.Q.UpdateDnsRecord(r.Context(), dbq.UpdateDnsRecordParams{
		ID: id, Name: req.Name,
		TTLSet: req.ttlSet, TTL: req.TTL,
		Data:    req.Data,
		ViewSet: req.viewSet, ViewID: req.ViewID,
		HCSet:   req.hcSet, HealthCheckID: req.HealthCheckID,
		DescriptionSet: req.descriptionSet, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "record not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "dns_record.update", TargetType: "dns_record", TargetID: id.String()})
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteRecord(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetDnsRecordFabricID(ctx, id)
	}, "record not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "dns:records:delete") {
		return
	}
	zf, err := h.Q.GetZoneFrozenByRecord(r.Context(), id)
	if err != nil {
		mapErr(w, err, "record not found")
		return
	}
	if zf.Frozen {
		httpx.Error(w, http.StatusUnprocessableEntity, errZoneFrozen.Error())
		return
	}
	if err := h.Q.DeleteDnsRecord(r.Context(), id); err != nil {
		mapErr(w, err, "record not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "dns_record.delete", TargetType: "dns_record", TargetID: id.String()})
	w.WriteHeader(http.StatusNoContent)
}

// ---- DNS Servers ----

type serverCreateReq struct {
	Name           string     `json:"name"`
	SiteID         uuid.UUID  `json:"site_id"`
	FabricID       uuid.UUID  `json:"fabric_id"`
	Role           string     `json:"role"`
	UnicastIP      string     `json:"unicast_ip"`
	Enabled        *bool      `json:"enabled"`
	AnycastGroupID *uuid.UUID `json:"anycast_group_id"`
}

func (h *Handler) createServer(w http.ResponseWriter, r *http.Request) {
	var req serverCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.Name == "" || req.SiteID == uuid.Nil || req.FabricID == uuid.Nil ||
		req.Role == "" || req.UnicastIP == "" {
		httpx.Error(w, http.StatusBadRequest, "name, site_id, fabric_id, role, unicast_ip required")
		return
	}
	if !h.enforceFabric(w, r, req.FabricID, "dns:servers:create") {
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	out, err := h.Q.CreateDnsServerRow(r.Context(), dbq.CreateDnsServerRowParams{
		Name: req.Name, SiteID: req.SiteID, FabricID: req.FabricID,
		Role: req.Role, UnicastIP: req.UnicastIP, Enabled: enabled,
		AnycastGroupID: req.AnycastGroupID,
	})
	if err != nil {
		mapErr(w, err, "server not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "dns_server.create", TargetType: "dns_server", TargetID: out.ID.String()})
	httpx.JSON(w, http.StatusCreated, out)
}

type serverUpdateReq struct {
	Name           *string
	Enabled        *bool
	UnicastIP      *string
	AnycastGroupID *uuid.UUID
	agSet          bool
}

func (u *serverUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["name"]; ok {
		_ = json.Unmarshal(v, &u.Name)
	}
	if v, ok := raw["enabled"]; ok {
		_ = json.Unmarshal(v, &u.Enabled)
	}
	if v, ok := raw["unicast_ip"]; ok {
		_ = json.Unmarshal(v, &u.UnicastIP)
	}
	if v, ok := raw["anycast_group_id"]; ok {
		u.agSet = true
		_ = json.Unmarshal(v, &u.AnycastGroupID)
	}
	return nil
}

func (h *Handler) updateServer(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetDnsServerFabricID(ctx, id)
	}, "server not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "dns:servers:update") {
		return
	}
	var req serverUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdateDnsServerRow(r.Context(), dbq.UpdateDnsServerRowParams{
		ID: id, Name: req.Name, Enabled: req.Enabled, UnicastIP: req.UnicastIP,
		AGSet: req.agSet, AnycastGroupID: req.AnycastGroupID,
	})
	if err != nil {
		mapErr(w, err, "server not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "dns_server.update", TargetType: "dns_server", TargetID: id.String()})
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteServer(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetDnsServerFabricID(ctx, id)
	}, "server not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "dns:servers:delete") {
		return
	}
	if err := h.Q.DeleteDnsServerRow(r.Context(), id); err != nil {
		mapErr(w, err, "server not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "dns_server.delete", TargetType: "dns_server", TargetID: id.String()})
	w.WriteHeader(http.StatusNoContent)
}

// ---- Anycast groups ----

type acGroupCreateReq struct {
	Name        string    `json:"name"`
	FabricID    uuid.UUID `json:"fabric_id"`
	Service     string    `json:"service"`
	AnycastIPv4 *string   `json:"anycast_ipv4"`
	AnycastIPv6 *string   `json:"anycast_ipv6"`
	Description *string   `json:"description"`
}

func (h *Handler) createAnycastGroup(w http.ResponseWriter, r *http.Request) {
	var req acGroupCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.Name == "" || req.FabricID == uuid.Nil || req.Service == "" {
		httpx.Error(w, http.StatusBadRequest, "name, fabric_id, service required")
		return
	}
	if !h.enforceFabric(w, r, req.FabricID, "dns:anycast-groups:create") {
		return
	}
	out, err := h.Q.CreateAnycastGroup(r.Context(), dbq.CreateAnycastGroupParams{
		Name: req.Name, FabricID: req.FabricID, Service: req.Service,
		AnycastIPv4: req.AnycastIPv4, AnycastIPv6: req.AnycastIPv6, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "anycast group not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "anycast_group.create", TargetType: "anycast_group", TargetID: out.ID.String()})
	httpx.JSON(w, http.StatusCreated, out)
}

type acGroupUpdateReq struct {
	Name           *string
	AnycastIPv4    *string
	v4Set          bool
	AnycastIPv6    *string
	v6Set          bool
	Description    *string
	descriptionSet bool
}

func (u *acGroupUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["name"]; ok {
		_ = json.Unmarshal(v, &u.Name)
	}
	if v, ok := raw["anycast_ipv4"]; ok {
		u.v4Set = true
		_ = json.Unmarshal(v, &u.AnycastIPv4)
	}
	if v, ok := raw["anycast_ipv6"]; ok {
		u.v6Set = true
		_ = json.Unmarshal(v, &u.AnycastIPv6)
	}
	if v, ok := raw["description"]; ok {
		u.descriptionSet = true
		_ = json.Unmarshal(v, &u.Description)
	}
	return nil
}

func (h *Handler) updateAnycastGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetAnycastGroupFabricID(ctx, id)
	}, "anycast group not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "dns:anycast-groups:update") {
		return
	}
	var req acGroupUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdateAnycastGroup(r.Context(), dbq.UpdateAnycastGroupParams{
		ID: id, Name: req.Name,
		V4Set: req.v4Set, AnycastIPv4: req.AnycastIPv4,
		V6Set: req.v6Set, AnycastIPv6: req.AnycastIPv6,
		DescriptionSet: req.descriptionSet, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "anycast group not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "anycast_group.update", TargetType: "anycast_group", TargetID: id.String()})
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteAnycastGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetAnycastGroupFabricID(ctx, id)
	}, "anycast group not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "dns:anycast-groups:delete") {
		return
	}
	if err := h.Q.DeleteAnycastGroup(r.Context(), id); err != nil {
		mapErr(w, err, "anycast group not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "anycast_group.delete", TargetType: "anycast_group", TargetID: id.String()})
	w.WriteHeader(http.StatusNoContent)
}

// ---- DNS Forwarders ----

type forwarderCreateReq struct {
	Name        string          `json:"name"`
	FabricID    uuid.UUID       `json:"fabric_id"`
	ZonePattern string          `json:"zone_pattern"`
	Upstreams   json.RawMessage `json:"upstreams"`
	Description *string         `json:"description"`
}

func (h *Handler) createForwarder(w http.ResponseWriter, r *http.Request) {
	var req forwarderCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.Name == "" || req.FabricID == uuid.Nil || req.ZonePattern == "" {
		httpx.Error(w, http.StatusBadRequest, "name, fabric_id, zone_pattern required")
		return
	}
	if !h.enforceFabric(w, r, req.FabricID, "dns:forwarders:create") {
		return
	}
	if len(req.Upstreams) == 0 {
		req.Upstreams = []byte("[]")
	}
	out, err := h.Q.CreateDnsForwarder(r.Context(), dbq.CreateDnsForwarderParams{
		Name: req.Name, FabricID: req.FabricID, ZonePattern: req.ZonePattern,
		Upstreams: req.Upstreams, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "forwarder not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "dns_forwarder.create", TargetType: "dns_forwarder", TargetID: out.ID.String()})
	httpx.JSON(w, http.StatusCreated, out)
}

type forwarderUpdateReq struct {
	Name           *string
	ZonePattern    *string
	Upstreams      json.RawMessage
	Description    *string
	descriptionSet bool
}

func (u *forwarderUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["name"]; ok {
		_ = json.Unmarshal(v, &u.Name)
	}
	if v, ok := raw["zone_pattern"]; ok {
		_ = json.Unmarshal(v, &u.ZonePattern)
	}
	if v, ok := raw["upstreams"]; ok {
		u.Upstreams = v
	}
	if v, ok := raw["description"]; ok {
		u.descriptionSet = true
		_ = json.Unmarshal(v, &u.Description)
	}
	return nil
}

func (h *Handler) updateForwarder(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetDnsForwarderFabricID(ctx, id)
	}, "forwarder not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "dns:forwarders:update") {
		return
	}
	var req forwarderUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdateDnsForwarder(r.Context(), dbq.UpdateDnsForwarderParams{
		ID: id, Name: req.Name, ZonePattern: req.ZonePattern, Upstreams: req.Upstreams,
		DescriptionSet: req.descriptionSet, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "forwarder not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "dns_forwarder.update", TargetType: "dns_forwarder", TargetID: id.String()})
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteForwarder(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetDnsForwarderFabricID(ctx, id)
	}, "forwarder not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "dns:forwarders:delete") {
		return
	}
	if err := h.Q.DeleteDnsForwarder(r.Context(), id); err != nil {
		mapErr(w, err, "forwarder not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "dns_forwarder.delete", TargetType: "dns_forwarder", TargetID: id.String()})
	w.WriteHeader(http.StatusNoContent)
}

// ---- DNS Catalog Zones ----

type catalogCreateReq struct {
	FabricID uuid.UUID `json:"fabric_id"`
	Name     string    `json:"name"`
	Enabled  *bool     `json:"enabled"`
}

func (h *Handler) createCatalogZone(w http.ResponseWriter, r *http.Request) {
	var req catalogCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.FabricID == uuid.Nil || req.Name == "" {
		httpx.Error(w, http.StatusBadRequest, "fabric_id and name required")
		return
	}
	if !h.enforceFabric(w, r, req.FabricID, "dns:catalog-zones:create") {
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	out, err := h.Q.CreateDnsCatalogZone(r.Context(), dbq.CreateDnsCatalogZoneParams{
		FabricID: req.FabricID, Name: req.Name, Enabled: enabled,
	})
	if err != nil {
		mapErr(w, err, "catalog zone not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "dns_catalog_zone.create", TargetType: "dns_catalog_zone", TargetID: out.ID.String()})
	httpx.JSON(w, http.StatusCreated, out)
}

type catalogUpdateReq struct {
	Name    *string `json:"name,omitempty"`
	Enabled *bool   `json:"enabled,omitempty"`
}

func (h *Handler) updateCatalogZone(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetDnsCatalogZoneFabricID(ctx, id)
	}, "catalog zone not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "dns:catalog-zones:update") {
		return
	}
	var req catalogUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdateDnsCatalogZone(r.Context(), dbq.UpdateDnsCatalogZoneParams{
		ID: id, Name: req.Name, Enabled: req.Enabled,
	})
	if err != nil {
		mapErr(w, err, "catalog zone not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "dns_catalog_zone.update", TargetType: "dns_catalog_zone", TargetID: id.String()})
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteCatalogZone(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetDnsCatalogZoneFabricID(ctx, id)
	}, "catalog zone not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "dns:catalog-zones:delete") {
		return
	}
	if err := h.Q.DeleteDnsCatalogZone(r.Context(), id); err != nil {
		mapErr(w, err, "catalog zone not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "dns_catalog_zone.delete", TargetType: "dns_catalog_zone", TargetID: id.String()})
	w.WriteHeader(http.StatusNoContent)
}

// ---- DNS Blocklists ----

type blocklistCreateReq struct {
	Name        string    `json:"name"`
	FabricID    uuid.UUID `json:"fabric_id"`
	Action      string    `json:"action"`
	SinkIPv4    *string   `json:"sink_ipv4"`
	SinkIPv6    *string   `json:"sink_ipv6"`
	Enabled     *bool     `json:"enabled"`
	Description *string   `json:"description"`
}

func (h *Handler) createBlocklist(w http.ResponseWriter, r *http.Request) {
	var req blocklistCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.Name == "" || req.FabricID == uuid.Nil || req.Action == "" {
		httpx.Error(w, http.StatusBadRequest, "name, fabric_id, action required")
		return
	}
	if !h.enforceFabric(w, r, req.FabricID, "dns:blocklists:create") {
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	out, err := h.Q.CreateDnsBlocklist(r.Context(), dbq.CreateDnsBlocklistParams{
		Name: req.Name, FabricID: req.FabricID, Action: req.Action,
		SinkIPv4: req.SinkIPv4, SinkIPv6: req.SinkIPv6,
		Enabled: enabled, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "blocklist not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "dns_blocklist.create", TargetType: "dns_blocklist", TargetID: out.ID.String()})
	httpx.JSON(w, http.StatusCreated, out)
}

type blocklistUpdateReq struct {
	Name           *string
	Action         *string
	SinkIPv4       *string
	v4Set          bool
	SinkIPv6       *string
	v6Set          bool
	Enabled        *bool
	Description    *string
	descriptionSet bool
}

func (u *blocklistUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["name"]; ok {
		_ = json.Unmarshal(v, &u.Name)
	}
	if v, ok := raw["action"]; ok {
		_ = json.Unmarshal(v, &u.Action)
	}
	if v, ok := raw["sink_ipv4"]; ok {
		u.v4Set = true
		_ = json.Unmarshal(v, &u.SinkIPv4)
	}
	if v, ok := raw["sink_ipv6"]; ok {
		u.v6Set = true
		_ = json.Unmarshal(v, &u.SinkIPv6)
	}
	if v, ok := raw["enabled"]; ok {
		_ = json.Unmarshal(v, &u.Enabled)
	}
	if v, ok := raw["description"]; ok {
		u.descriptionSet = true
		_ = json.Unmarshal(v, &u.Description)
	}
	return nil
}

func (h *Handler) updateBlocklist(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetDnsBlocklistFabricID(ctx, id)
	}, "blocklist not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "dns:blocklists:update") {
		return
	}
	var req blocklistUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdateDnsBlocklist(r.Context(), dbq.UpdateDnsBlocklistParams{
		ID: id, Name: req.Name, Action: req.Action,
		V4Set: req.v4Set, SinkIPv4: req.SinkIPv4,
		V6Set: req.v6Set, SinkIPv6: req.SinkIPv6,
		Enabled: req.Enabled,
		DescriptionSet: req.descriptionSet, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "blocklist not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "dns_blocklist.update", TargetType: "dns_blocklist", TargetID: id.String()})
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteBlocklist(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetDnsBlocklistFabricID(ctx, id)
	}, "blocklist not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "dns:blocklists:delete") {
		return
	}
	if err := h.Q.DeleteDnsBlocklist(r.Context(), id); err != nil {
		mapErr(w, err, "blocklist not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "dns_blocklist.delete", TargetType: "dns_blocklist", TargetID: id.String()})
	w.WriteHeader(http.StatusNoContent)
}

// ---- Blocklist entries ----

type blocklistEntryCreateReq struct {
	Pattern     string  `json:"pattern"`
	Description *string `json:"description"`
}

func (h *Handler) createBlocklistEntry(w http.ResponseWriter, r *http.Request) {
	blID, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	// Blocklist-entry create is gated under dns:blocklists:update (route
	// definition), so resolve the parent blocklist's fabric and enforce
	// that scope.
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetDnsBlocklistFabricID(ctx, blID)
	}, "blocklist not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "dns:blocklists:update") {
		return
	}
	var req blocklistEntryCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Pattern == "" {
		httpx.Error(w, http.StatusBadRequest, "pattern required")
		return
	}
	out, err := h.Q.CreateDnsBlocklistEntry(r.Context(), dbq.CreateDnsBlocklistEntryParams{
		BlocklistID: blID, Pattern: req.Pattern, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "entry not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "dns_blocklist_entry.create", TargetType: "dns_blocklist_entry", TargetID: out.ID.String()})
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *Handler) deleteBlocklistEntry(w http.ResponseWriter, r *http.Request) {
	entryID, ok := idFromURL(w, r, "entry_id")
	if !ok {
		return
	}
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetDnsBlocklistEntryFabricID(ctx, entryID)
	}, "entry not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "dns:blocklists:update") {
		return
	}
	if err := h.Q.DeleteDnsBlocklistEntry(r.Context(), entryID); err != nil {
		mapErr(w, err, "entry not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "dns_blocklist_entry.delete", TargetType: "dns_blocklist_entry", TargetID: entryID.String()})
	w.WriteHeader(http.StatusNoContent)
}

// ---- DNS Views ----

type viewCreateReq struct {
	Name        string          `json:"name"`
	FabricID    uuid.UUID       `json:"fabric_id"`
	MatchCidrs  json.RawMessage `json:"match_cidrs"`
	Priority    *int32          `json:"priority"`
	Description *string         `json:"description"`
}

func (h *Handler) createView(w http.ResponseWriter, r *http.Request) {
	var req viewCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.Name == "" || req.FabricID == uuid.Nil {
		httpx.Error(w, http.StatusBadRequest, "name and fabric_id required")
		return
	}
	if !h.enforceFabric(w, r, req.FabricID, "dns:views:create") {
		return
	}
	if len(req.MatchCidrs) == 0 {
		req.MatchCidrs = []byte("[]")
	}
	priority := int32(100)
	if req.Priority != nil {
		priority = *req.Priority
	}
	out, err := h.Q.CreateDnsView(r.Context(), dbq.CreateDnsViewParams{
		Name: req.Name, FabricID: req.FabricID, MatchCidrs: req.MatchCidrs,
		Priority: priority, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "view not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "dns_view.create", TargetType: "dns_view", TargetID: out.ID.String()})
	httpx.JSON(w, http.StatusCreated, out)
}

type viewUpdateReq struct {
	Name           *string
	MatchCidrs     json.RawMessage
	Priority       *int32
	Description    *string
	descriptionSet bool
}

func (u *viewUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["name"]; ok {
		_ = json.Unmarshal(v, &u.Name)
	}
	if v, ok := raw["match_cidrs"]; ok {
		u.MatchCidrs = v
	}
	if v, ok := raw["priority"]; ok {
		_ = json.Unmarshal(v, &u.Priority)
	}
	if v, ok := raw["description"]; ok {
		u.descriptionSet = true
		_ = json.Unmarshal(v, &u.Description)
	}
	return nil
}

func (h *Handler) updateView(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetDnsViewFabricID(ctx, id)
	}, "view not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "dns:views:update") {
		return
	}
	var req viewUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdateDnsView(r.Context(), dbq.UpdateDnsViewParams{
		ID: id, Name: req.Name, MatchCidrs: req.MatchCidrs, Priority: req.Priority,
		DescriptionSet: req.descriptionSet, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "view not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "dns_view.update", TargetType: "dns_view", TargetID: id.String()})
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteView(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetDnsViewFabricID(ctx, id)
	}, "view not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "dns:views:delete") {
		return
	}
	if err := h.Q.DeleteDnsView(r.Context(), id); err != nil {
		mapErr(w, err, "view not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "dns_view.delete", TargetType: "dns_view", TargetID: id.String()})
	w.WriteHeader(http.StatusNoContent)
}

// ---- DNS Health Checks ----

type hcCreateReq struct {
	Name            string    `json:"name"`
	FabricID        uuid.UUID `json:"fabric_id"`
	TargetIP        string    `json:"target_ip"`
	Protocol        string    `json:"protocol"`
	Port            *int32    `json:"port"`
	Path            string    `json:"path"`
	IntervalSeconds *int32    `json:"interval_seconds"`
	TimeoutSeconds  *int32    `json:"timeout_seconds"`
	Enabled         *bool     `json:"enabled"`
}

func (h *Handler) createHealthCheck(w http.ResponseWriter, r *http.Request) {
	var req hcCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.Name == "" || req.FabricID == uuid.Nil || req.TargetIP == "" || req.Protocol == "" {
		httpx.Error(w, http.StatusBadRequest, "name, fabric_id, target_ip, protocol required")
		return
	}
	if !h.enforceFabric(w, r, req.FabricID, "dns:health-checks:create") {
		return
	}
	if req.Path == "" {
		req.Path = "/"
	}
	interval := int32(30)
	if req.IntervalSeconds != nil {
		interval = *req.IntervalSeconds
	}
	timeout := int32(5)
	if req.TimeoutSeconds != nil {
		timeout = *req.TimeoutSeconds
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	out, err := h.Q.CreateDnsHealthCheck(r.Context(), dbq.CreateDnsHealthCheckParams{
		Name: req.Name, FabricID: req.FabricID, TargetIP: req.TargetIP,
		Protocol: req.Protocol, Port: req.Port, Path: req.Path,
		IntervalSeconds: interval, TimeoutSeconds: timeout, Enabled: enabled,
	})
	if err != nil {
		mapErr(w, err, "health check not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "dns_health_check.create", TargetType: "dns_health_check", TargetID: out.ID.String()})
	httpx.JSON(w, http.StatusCreated, out)
}

type hcUpdateReq struct {
	Name            *string
	TargetIP        *string
	Protocol        *string
	Port            *int32
	portSet         bool
	Path            *string
	IntervalSeconds *int32
	TimeoutSeconds  *int32
	Enabled         *bool
}

func (u *hcUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for k, dst := range map[string]any{
		"name": &u.Name, "target_ip": &u.TargetIP, "protocol": &u.Protocol,
		"path": &u.Path, "interval_seconds": &u.IntervalSeconds,
		"timeout_seconds": &u.TimeoutSeconds, "enabled": &u.Enabled,
	} {
		if v, ok := raw[k]; ok {
			_ = json.Unmarshal(v, dst)
		}
	}
	if v, ok := raw["port"]; ok {
		u.portSet = true
		_ = json.Unmarshal(v, &u.Port)
	}
	return nil
}

func (h *Handler) updateHealthCheck(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetDnsHealthCheckFabricID(ctx, id)
	}, "health check not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "dns:health-checks:update") {
		return
	}
	var req hcUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdateDnsHealthCheck(r.Context(), dbq.UpdateDnsHealthCheckParams{
		ID: id, Name: req.Name, TargetIP: req.TargetIP, Protocol: req.Protocol,
		PortSet: req.portSet, Port: req.Port, Path: req.Path,
		IntervalSeconds: req.IntervalSeconds, TimeoutSeconds: req.TimeoutSeconds,
		Enabled: req.Enabled,
	})
	if err != nil {
		mapErr(w, err, "health check not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "dns_health_check.update", TargetType: "dns_health_check", TargetID: id.String()})
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteHealthCheck(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetDnsHealthCheckFabricID(ctx, id)
	}, "health check not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "dns:health-checks:delete") {
		return
	}
	if err := h.Q.DeleteDnsHealthCheck(r.Context(), id); err != nil {
		mapErr(w, err, "health check not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "dns_health_check.delete", TargetType: "dns_health_check", TargetID: id.String()})
	w.WriteHeader(http.StatusNoContent)
}

// ---- BGP Peers ----

type bgpPeerCreateReq struct {
	Name            string     `json:"name"`
	SiteID          uuid.UUID  `json:"site_id"`
	LocalAsnID      uuid.UUID  `json:"local_asn_id"`
	PeerAsnID       uuid.UUID  `json:"peer_asn_id"`
	PeerIP          string     `json:"peer_ip"`
	PeerDescription *string    `json:"peer_description"`
	TcpAoKeyChainID *uuid.UUID `json:"tcp_ao_key_chain_id"`
	Enabled         *bool      `json:"enabled"`
}

func (h *Handler) createBgpPeer(w http.ResponseWriter, r *http.Request) {
	var req bgpPeerCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.Name == "" || req.SiteID == uuid.Nil || req.LocalAsnID == uuid.Nil ||
		req.PeerAsnID == uuid.Nil || req.PeerIP == "" {
		httpx.Error(w, http.StatusBadRequest, "name, site_id, local_asn_id, peer_asn_id, peer_ip required")
		return
	}
	if !h.enforceSite(w, r, req.SiteID, "dns:bgp-peers:create") {
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	out, err := h.Q.CreateBgpPeer(r.Context(), dbq.CreateBgpPeerParams{
		Name: req.Name, SiteID: req.SiteID,
		LocalAsnID: req.LocalAsnID, PeerAsnID: req.PeerAsnID,
		PeerIP: req.PeerIP, PeerDescription: req.PeerDescription,
		TcpAoKeyChainID: req.TcpAoKeyChainID, Enabled: enabled,
	})
	if err != nil {
		mapErr(w, err, "bgp peer not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "bgp_peer.create", TargetType: "bgp_peer", TargetID: out.ID.String()})
	httpx.JSON(w, http.StatusCreated, out)
}

type bgpPeerUpdateReq struct {
	Name            *string
	LocalAsnID      *uuid.UUID
	PeerAsnID       *uuid.UUID
	PeerIP          *string
	PeerDescription *string
	descSet         bool
	TcpAoKeyChainID *uuid.UUID
	aoSet           bool
	Enabled         *bool
}

func (u *bgpPeerUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for k, dst := range map[string]any{
		"name": &u.Name, "local_asn_id": &u.LocalAsnID, "peer_asn_id": &u.PeerAsnID,
		"peer_ip": &u.PeerIP, "enabled": &u.Enabled,
	} {
		if v, ok := raw[k]; ok {
			_ = json.Unmarshal(v, dst)
		}
	}
	if v, ok := raw["peer_description"]; ok {
		u.descSet = true
		_ = json.Unmarshal(v, &u.PeerDescription)
	}
	if v, ok := raw["tcp_ao_key_chain_id"]; ok {
		u.aoSet = true
		_ = json.Unmarshal(v, &u.TcpAoKeyChainID)
	}
	return nil
}

func (h *Handler) updateBgpPeer(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	sid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetBgpPeerSiteID(ctx, id)
	}, "bgp peer not found")
	if !ok {
		return
	}
	if !h.enforceSite(w, r, sid, "dns:bgp-peers:update") {
		return
	}
	var req bgpPeerUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdateBgpPeer(r.Context(), dbq.UpdateBgpPeerParams{
		ID: id, Name: req.Name,
		LocalAsnID: req.LocalAsnID, PeerAsnID: req.PeerAsnID, PeerIP: req.PeerIP,
		DescSet: req.descSet, PeerDescription: req.PeerDescription,
		AOSet: req.aoSet, TcpAoKeyChainID: req.TcpAoKeyChainID,
		Enabled: req.Enabled,
	})
	if err != nil {
		mapErr(w, err, "bgp peer not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "bgp_peer.update", TargetType: "bgp_peer", TargetID: id.String()})
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteBgpPeer(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	sid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetBgpPeerSiteID(ctx, id)
	}, "bgp peer not found")
	if !ok {
		return
	}
	if !h.enforceSite(w, r, sid, "dns:bgp-peers:delete") {
		return
	}
	if err := h.Q.DeleteBgpPeer(r.Context(), id); err != nil {
		mapErr(w, err, "bgp peer not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "bgp_peer.delete", TargetType: "bgp_peer", TargetID: id.String()})
	w.WriteHeader(http.StatusNoContent)
}

// ---- Anycast BGP Bindings ----

type bindingCreateReq struct {
	DnsServerID uuid.UUID `json:"dns_server_id"`
	BgpPeerID   uuid.UUID `json:"bgp_peer_id"`
}

func (h *Handler) createAnycastBinding(w http.ResponseWriter, r *http.Request) {
	var req bindingCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.DnsServerID == uuid.Nil || req.BgpPeerID == uuid.Nil {
		httpx.Error(w, http.StatusBadRequest, "dns_server_id and bgp_peer_id required")
		return
	}
	// Anycast bindings link a fabric-rooted dns_server to a site-rooted
	// bgp_peer. PR 58 enforces on the fabric side via the dns_server's
	// fabric_id — that's the scope a binding-mutating principal needs to
	// own. (A cross-fabric/cross-site combination is rejected by the
	// existing uq_anycast_binding constraint and downstream renderer
	// validation; bgp-peer-side site scope is not double-checked here.)
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetDnsServerFabricID(ctx, req.DnsServerID)
	}, "dns server not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "dns:anycast-bindings:create") {
		return
	}
	out, err := h.Q.CreateAnycastBinding(r.Context(), dbq.CreateAnycastBindingParams{
		DnsServerID: req.DnsServerID, BgpPeerID: req.BgpPeerID,
	})
	if err != nil {
		mapErr(w, err, "binding not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "anycast_binding.create", TargetType: "anycast_binding", TargetID: out.ID.String()})
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *Handler) deleteAnycastBinding(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetAnycastBindingDnsServerFabricID(ctx, id)
	}, "binding not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "dns:anycast-bindings:delete") {
		return
	}
	if err := h.Q.DeleteAnycastBinding(r.Context(), id); err != nil {
		mapErr(w, err, "binding not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "anycast_binding.delete", TargetType: "anycast_binding", TargetID: id.String()})
	w.WriteHeader(http.StatusNoContent)
}
