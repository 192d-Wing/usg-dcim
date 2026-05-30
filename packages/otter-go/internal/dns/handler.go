// Package dns holds GET handlers for the core DNS resources: zones
// (list+get), records (list). Deferred to follow-up PRs:
// zones/{id}/preview, /keys, /ds-records (depend on the dns rendering
// service — non-trivial); servers, anycast-groups, forwarders,
// catalog-zones, dashboard, blocklists, views, health-checks, etc.
package dns

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type Querier interface {
	ListDnsZones(ctx context.Context, arg dbq.ListDnsZonesParams) ([]dbq.DnsZone, error)
	CountDnsZones(ctx context.Context, arg dbq.CountDnsZonesParams) (int64, error)
	GetDnsZone(ctx context.Context, id uuid.UUID) (dbq.DnsZone, error)
	ListDnsRecords(ctx context.Context, arg dbq.ListDnsRecordsParams) ([]dbq.DnsRecord, error)
	CountDnsRecords(ctx context.Context, arg dbq.CountDnsRecordsParams) (int64, error)

	ListDnsServers(ctx context.Context, arg dbq.ListDnsServersParams) ([]dbq.DnsServer, error)
	CountDnsServers(ctx context.Context, arg dbq.CountDnsServersParams) (int64, error)
	GetDnsServer(ctx context.Context, id uuid.UUID) (dbq.DnsServer, error)
	ListAnycastGroups(ctx context.Context, arg dbq.ListAnycastGroupsParams) ([]dbq.AnycastGroup, error)
	CountAnycastGroups(ctx context.Context, arg dbq.CountAnycastGroupsParams) (int64, error)
	ListDnsForwarders(ctx context.Context, arg dbq.ListDnsForwardersParams) ([]dbq.DnsForwarder, error)
	CountDnsForwarders(ctx context.Context, arg dbq.CountDnsForwardersParams) (int64, error)
	ListDnsCatalogZones(ctx context.Context, arg dbq.ListDnsCatalogZonesParams) ([]dbq.DnsCatalogZone, error)
	CountDnsCatalogZones(ctx context.Context, arg dbq.CountDnsCatalogZonesParams) (int64, error)

	ListDnsBlocklists(ctx context.Context, arg dbq.ListDnsBlocklistsParams) ([]dbq.DnsBlocklist, error)
	CountDnsBlocklists(ctx context.Context, arg dbq.CountDnsBlocklistsParams) (int64, error)
	GetDnsBlocklist(ctx context.Context, id uuid.UUID) (dbq.DnsBlocklist, error)
	ListDnsBlocklistEntries(ctx context.Context, arg dbq.ListDnsBlocklistEntriesParams) ([]dbq.DnsBlocklistEntry, error)
	CountDnsBlocklistEntries(ctx context.Context, blocklistID uuid.UUID) (int64, error)
	ListDnsViews(ctx context.Context, arg dbq.ListDnsViewsParams) ([]dbq.DnsView, error)
	CountDnsViews(ctx context.Context, arg dbq.CountDnsViewsParams) (int64, error)
	ListDnsHealthChecks(ctx context.Context, arg dbq.ListDnsHealthChecksParams) ([]dbq.DnsHealthCheck, error)
	CountDnsHealthChecks(ctx context.Context, arg dbq.CountDnsHealthChecksParams) (int64, error)
	ListBgpPeers(ctx context.Context, arg dbq.ListBgpPeersParams) ([]dbq.BgpPeer, error)
	CountBgpPeers(ctx context.Context, arg dbq.CountBgpPeersParams) (int64, error)
	ListAnycastBindings(ctx context.Context, arg dbq.ListAnycastBindingsParams) ([]dbq.AnycastBgpBinding, error)
	CountAnycastBindings(ctx context.Context, arg dbq.CountAnycastBindingsParams) (int64, error)

	// Mutations (PR 43). Action endpoints (freeze/unfreeze, import,
	// sync-from-ipam, enable/disable-dnssec, nsec3, render-status,
	// health-checks/result, blocklists/entries/bulk) are deferred to a
	// follow-up.
	CreateDnsZone(ctx context.Context, arg dbq.CreateDnsZoneParams) (dbq.DnsZone, error)
	UpdateDnsZone(ctx context.Context, arg dbq.UpdateDnsZoneParams) (dbq.DnsZone, error)
	DeleteDnsZone(ctx context.Context, id uuid.UUID) error
	SetDnsZoneFrozen(ctx context.Context, id uuid.UUID, frozen bool) (dbq.DnsZone, error)
	SetDnsZoneNsec3(ctx context.Context, arg dbq.SetDnsZoneNsec3Params) (dbq.DnsZone, error)
	ListAllRecordsInZone(ctx context.Context, zoneID uuid.UUID) ([]dbq.DnsRecordForRender, error)
	SetDnsHealthCheckResult(ctx context.Context, id uuid.UUID, status string, lastError *string) (int64, error)
	SetDnsServerRenderStatus(ctx context.Context, arg dbq.SetDnsServerRenderStatusParams) (int64, error)
	CreateDnsServerMetricsSample(ctx context.Context, arg dbq.CreateDnsServerMetricsSampleParams) (dbq.DnsMetricsSampleRow, error)
	ListDnsServerMetricsSamples(ctx context.Context, serverID uuid.UUID, cutoff time.Time) ([]dbq.DnsMetricsSampleRow, error)
	ListDnsKeysByZone(ctx context.Context, zoneID uuid.UUID) ([]dbq.DnsKeyRow, error)
	CreateDnsKey(ctx context.Context, arg dbq.CreateDnsKeyParams) (dbq.DnsKeyRow, error)
	SetDnsZoneSigned(ctx context.Context, id uuid.UUID, signed bool) (int64, error)
	ListActiveDnsKeysForZoneAndRole(ctx context.Context, zoneID uuid.UUID, role string) ([]dbq.DnsKeyRow, error)
	RetireDnsKey(ctx context.Context, id uuid.UUID) (int64, error)
	DeleteDnsKey(ctx context.Context, id uuid.UUID) (int64, error)
	RetireAllDnsKeysForZone(ctx context.Context, zoneID uuid.UUID) (int64, error)
	DeleteAllDnsKeysForZone(ctx context.Context, zoneID uuid.UUID) ([]dbq.DnsKeyRow, error)
	GetDnsKey(ctx context.Context, id uuid.UUID) (dbq.DnsKeyRow, error)
	TouchDnsZone(ctx context.Context, id uuid.UUID) (int64, error)
	DeleteManualRecordsInZone(ctx context.Context, zoneID uuid.UUID) ([]uuid.UUID, error)
	UpdateDnsZoneSoa(ctx context.Context, arg dbq.UpdateDnsZoneSoaParams) error
	ListReverseZonesForSite(ctx context.Context, fabricID, siteID uuid.UUID) ([]dbq.DnsZone, error)
	GetReverseZoneByName(ctx context.Context, fabricID, siteID uuid.UUID, name string) (dbq.DnsZone, error)
	CreateReverseZone(ctx context.Context, name string, fabricID, siteID uuid.UUID) (dbq.DnsZone, error)
	ListIPAddressesForSiteWithDnsName(ctx context.Context, siteID uuid.UUID) ([]dbq.IPAddressForSyncRow, error)
	DeleteIPAMRecordsInZones(ctx context.Context, zoneIDs []uuid.UUID) error
	CountIPAMRecordsInZones(ctx context.Context, zoneIDs []uuid.UUID) (int64, error)
	CreateProjectedDnsRecord(ctx context.Context, arg dbq.CreateProjectedDnsRecordParams) (uuid.UUID, error)
	ListDnsSamplesInWindow(ctx context.Context, cutoff time.Time, serverIDs []uuid.UUID) ([]dbq.DnsMetricsSampleRow, error)
	ListDnsServersForDashboard(ctx context.Context, fabricID *uuid.UUID) ([]dbq.DnsServerForDashboardRow, error)
	ListDnsZonesForDashboard(ctx context.Context, fabricID *uuid.UUID) ([]dbq.DnsZoneForDashboardRow, error)
	CountAnycastGroupsForDashboard(ctx context.Context, fabricID *uuid.UUID) (int64, error)
	CreateDnsRecord(ctx context.Context, arg dbq.CreateDnsRecordParams) (dbq.DnsRecord, error)
	UpdateDnsRecord(ctx context.Context, arg dbq.UpdateDnsRecordParams) (dbq.DnsRecord, error)
	DeleteDnsRecord(ctx context.Context, id uuid.UUID) error
	CreateDnsServerRow(ctx context.Context, arg dbq.CreateDnsServerRowParams) (dbq.DnsServer, error)
	UpdateDnsServerRow(ctx context.Context, arg dbq.UpdateDnsServerRowParams) (dbq.DnsServer, error)
	DeleteDnsServerRow(ctx context.Context, id uuid.UUID) error
	CreateAnycastGroup(ctx context.Context, arg dbq.CreateAnycastGroupParams) (dbq.AnycastGroup, error)
	UpdateAnycastGroup(ctx context.Context, arg dbq.UpdateAnycastGroupParams) (dbq.AnycastGroup, error)
	DeleteAnycastGroup(ctx context.Context, id uuid.UUID) error
	CreateDnsForwarder(ctx context.Context, arg dbq.CreateDnsForwarderParams) (dbq.DnsForwarder, error)
	UpdateDnsForwarder(ctx context.Context, arg dbq.UpdateDnsForwarderParams) (dbq.DnsForwarder, error)
	DeleteDnsForwarder(ctx context.Context, id uuid.UUID) error
	CreateDnsCatalogZone(ctx context.Context, arg dbq.CreateDnsCatalogZoneParams) (dbq.DnsCatalogZone, error)
	UpdateDnsCatalogZone(ctx context.Context, arg dbq.UpdateDnsCatalogZoneParams) (dbq.DnsCatalogZone, error)
	DeleteDnsCatalogZone(ctx context.Context, id uuid.UUID) error
	CreateDnsBlocklist(ctx context.Context, arg dbq.CreateDnsBlocklistParams) (dbq.DnsBlocklist, error)
	UpdateDnsBlocklist(ctx context.Context, arg dbq.UpdateDnsBlocklistParams) (dbq.DnsBlocklist, error)
	DeleteDnsBlocklist(ctx context.Context, id uuid.UUID) error
	CreateDnsBlocklistEntry(ctx context.Context, arg dbq.CreateDnsBlocklistEntryParams) (dbq.DnsBlocklistEntry, error)
	DeleteDnsBlocklistEntry(ctx context.Context, id uuid.UUID) error
	CreateDnsView(ctx context.Context, arg dbq.CreateDnsViewParams) (dbq.DnsView, error)
	UpdateDnsView(ctx context.Context, arg dbq.UpdateDnsViewParams) (dbq.DnsView, error)
	DeleteDnsView(ctx context.Context, id uuid.UUID) error
	CreateDnsHealthCheck(ctx context.Context, arg dbq.CreateDnsHealthCheckParams) (dbq.DnsHealthCheck, error)
	UpdateDnsHealthCheck(ctx context.Context, arg dbq.UpdateDnsHealthCheckParams) (dbq.DnsHealthCheck, error)
	DeleteDnsHealthCheck(ctx context.Context, id uuid.UUID) error
	CreateBgpPeer(ctx context.Context, arg dbq.CreateBgpPeerParams) (dbq.BgpPeer, error)
	UpdateBgpPeer(ctx context.Context, arg dbq.UpdateBgpPeerParams) (dbq.BgpPeer, error)
	DeleteBgpPeer(ctx context.Context, id uuid.UUID) error
	CreateAnycastBinding(ctx context.Context, arg dbq.CreateAnycastBindingParams) (dbq.AnycastBgpBinding, error)
	DeleteAnycastBinding(ctx context.Context, id uuid.UUID) error

	// Invariants (PR 52)
	GetZoneFrozenByRecord(ctx context.Context, recordID uuid.UUID) (dbq.ZoneFrozenRow, error)

	// ABAC parent-fabric lookups (PR 57). Used by mutation handlers to
	// resolve {id} → fabric_id before EnforceFabricScope on update/
	// delete paths, and to resolve parent-id → fabric_id on the 2-hop
	// dns_records / dns_blocklist_entries create paths via their parent
	// (zone / blocklist) lookups above.
	GetDnsZoneFabricID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetDnsRecordFabricID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetDnsServerFabricID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetAnycastGroupFabricID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetDnsForwarderFabricID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetDnsCatalogZoneFabricID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetDnsBlocklistFabricID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetDnsBlocklistEntryFabricID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetDnsViewFabricID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetDnsHealthCheckFabricID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)

	// PR 58 — BGP peers (site-scoped) + anycast bindings (fabric-scoped
	// via dns_server). EnforceSiteScope walks region/site-group via the
	// two lookups below; they're shared with sites/racks/assets handlers.
	GetBgpPeerSiteID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetAnycastBindingDnsServerFabricID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetSiteRegionID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetSiteOrganizationID(ctx context.Context, id uuid.UUID) (*uuid.UUID, error)
	ListSiteGroupIDsForSite(ctx context.Context, siteID uuid.UUID) ([]uuid.UUID, error)

	// PR 63: scope-filtered LIST. Site-scope expansion for /dns/bgp-peers.
	ListSiteIDsForExpansion(ctx context.Context, arg dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error)
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
// non-nil non-empty slice for a fabric-scoped caller. Same shape as
// the helper in internal/ipam (PR 56).
func scopedListFilter(r *http.Request, capCode string) (ids []uuid.UUID, ok bool) {
	p, _ := auth.From(r.Context())
	ids, scoped := auth.ScopedFabricFilter(p, capCode)
	if scoped && len(ids) == 0 {
		return nil, false
	}
	return ids, true
}

func (h *Handler) Mount(r chi.Router) {
	// Python mounts the dns router with prefix `/dns`.
	r.Route("/dns", func(r chi.Router) {
		r.Get("/zones", h.listZones)
		r.Get("/zones/{id}", h.getZone)
		r.Get("/records", h.listRecords)
		r.Get("/servers", h.listServers)
		r.Get("/servers/{id}", h.getServer)
		r.Get("/anycast-groups", h.listAnycastGroups)
		r.Get("/forwarders", h.listForwarders)
		r.Get("/catalog-zones", h.listCatalogZones)
		r.Get("/blocklists", h.listBlocklists)
		r.Get("/blocklists/{id}/entries", h.listBlocklistEntries)
		r.Get("/views", h.listViews)
		r.Get("/health-checks", h.listHealthChecks)
		r.Get("/bgp-peers", h.listBgpPeers)
		r.Get("/anycast-bindings", h.listAnycastBindings)

		// ---- Mutations (PR 43) ----
		r.With(auth.RequireCapability("dns:zones:create")).Post("/zones", h.createZone)
		r.With(auth.RequireCapability("dns:zones:update")).Patch("/zones/{id}", h.updateZone)
		r.With(auth.RequireCapability("dns:zones:delete")).Delete("/zones/{id}", h.deleteZone)
		r.With(auth.RequireCapability("dns:zones:update")).Post("/zones/{id}/freeze", h.freezeZone)
		r.With(auth.RequireCapability("dns:zones:update")).Post("/zones/{id}/unfreeze", h.unfreezeZone)
		r.With(auth.RequireCapability("dns:zones:update")).Post("/zones/{id}/nsec3", h.setZoneNsec3)
		r.With(auth.RequireCapability("dns:zones:update")).Delete("/zones/{id}/nsec3", h.clearZoneNsec3)
		r.With(auth.RequireCapability("dns:zones:read")).Get("/zones/{id}/preview", h.previewZone)
		r.With(auth.RequireCapability("dns:keys:read")).Get("/zones/{id}/keys", h.listZoneKeys)
		r.With(auth.RequireCapability("dns:keys:read")).Get("/zones/{id}/ds-records", h.listZoneDsRecords)
		r.With(auth.RequireCapability("dns:keys:rotate")).Post("/zones/{id}/enable-dnssec", h.enableDnssec)
		r.With(auth.RequireCapability("dns:keys:rotate")).Post("/zones/{id}/disable-dnssec", h.disableDnssec)
		r.With(auth.RequireCapability("dns:keys:rotate")).Post("/zones/{id}/rotate-key/{role}", h.rotateZoneKey)
		r.With(auth.RequireCapability("dns:keys:delete")).Delete("/keys/{id}", h.deleteDnsKey)
		r.With(auth.RequireCapability("dns:zones:update")).Post("/zones/{id}/import", h.importZone)
		r.With(auth.RequireCapability("dns:zones:update")).Post("/zones/{id}/sync-from-ipam", h.syncFromIPAM)
		r.With(auth.RequireCapability("dns:servers:read")).Get("/dashboard", h.dnsDashboard)

		r.With(auth.RequireCapability("dns:records:create")).Post("/records", h.createRecord)
		r.With(auth.RequireCapability("dns:records:update")).Patch("/records/{id}", h.updateRecord)
		r.With(auth.RequireCapability("dns:records:delete")).Delete("/records/{id}", h.deleteRecord)

		r.With(auth.RequireCapability("dns:servers:create")).Post("/servers", h.createServer)
		r.With(auth.RequireCapability("dns:servers:update")).Patch("/servers/{id}", h.updateServer)
		r.With(auth.RequireCapability("dns:servers:delete")).Delete("/servers/{id}", h.deleteServer)
		r.With(auth.RequireCapability("dns:servers:update")).Post("/servers/{id}/render-status", h.postServerRenderStatus)
		r.With(auth.RequireCapability("dns:servers:update")).Post("/servers/{id}/metrics", h.postServerMetrics)
		r.With(auth.RequireCapability("dns:servers:read")).Get("/servers/{id}/metrics", h.listServerMetrics)

		r.With(auth.RequireCapability("dns:anycast-groups:create")).Post("/anycast-groups", h.createAnycastGroup)
		r.With(auth.RequireCapability("dns:anycast-groups:update")).Patch("/anycast-groups/{id}", h.updateAnycastGroup)
		r.With(auth.RequireCapability("dns:anycast-groups:delete")).Delete("/anycast-groups/{id}", h.deleteAnycastGroup)

		r.With(auth.RequireCapability("dns:forwarders:create")).Post("/forwarders", h.createForwarder)
		r.With(auth.RequireCapability("dns:forwarders:update")).Patch("/forwarders/{id}", h.updateForwarder)
		r.With(auth.RequireCapability("dns:forwarders:delete")).Delete("/forwarders/{id}", h.deleteForwarder)

		r.With(auth.RequireCapability("dns:catalog-zones:create")).Post("/catalog-zones", h.createCatalogZone)
		r.With(auth.RequireCapability("dns:catalog-zones:update")).Patch("/catalog-zones/{id}", h.updateCatalogZone)
		r.With(auth.RequireCapability("dns:catalog-zones:delete")).Delete("/catalog-zones/{id}", h.deleteCatalogZone)

		r.With(auth.RequireCapability("dns:blocklists:create")).Post("/blocklists", h.createBlocklist)
		r.With(auth.RequireCapability("dns:blocklists:update")).Patch("/blocklists/{id}", h.updateBlocklist)
		r.With(auth.RequireCapability("dns:blocklists:delete")).Delete("/blocklists/{id}", h.deleteBlocklist)
		r.With(auth.RequireCapability("dns:blocklists:update")).Post("/blocklists/{id}/entries", h.createBlocklistEntry)
		r.With(auth.RequireCapability("dns:blocklists:update")).Delete("/blocklists/{id}/entries/{entry_id}", h.deleteBlocklistEntry)

		r.With(auth.RequireCapability("dns:views:create")).Post("/views", h.createView)
		r.With(auth.RequireCapability("dns:views:update")).Patch("/views/{id}", h.updateView)
		r.With(auth.RequireCapability("dns:views:delete")).Delete("/views/{id}", h.deleteView)

		r.With(auth.RequireCapability("dns:health-checks:create")).Post("/health-checks", h.createHealthCheck)
		r.With(auth.RequireCapability("dns:health-checks:update")).Post("/health-checks/{id}/result", h.postHealthCheckResult)
		r.With(auth.RequireCapability("dns:health-checks:update")).Patch("/health-checks/{id}", h.updateHealthCheck)
		r.With(auth.RequireCapability("dns:health-checks:delete")).Delete("/health-checks/{id}", h.deleteHealthCheck)

		r.With(auth.RequireCapability("dns:bgp-peers:create")).Post("/bgp-peers", h.createBgpPeer)
		r.With(auth.RequireCapability("dns:bgp-peers:update")).Patch("/bgp-peers/{id}", h.updateBgpPeer)
		r.With(auth.RequireCapability("dns:bgp-peers:delete")).Delete("/bgp-peers/{id}", h.deleteBgpPeer)

		r.With(auth.RequireCapability("dns:anycast-bindings:create")).Post("/anycast-bindings", h.createAnycastBinding)
		r.With(auth.RequireCapability("dns:anycast-bindings:delete")).Delete("/anycast-bindings/{id}", h.deleteAnycastBinding)
	})
}

func fabricIDFromQuery(w http.ResponseWriter, q map[string][]string) (*uuid.UUID, bool) {
	v := first(q, "fabric_id")
	if v == "" {
		return nil, true
	}
	id, err := uuid.Parse(v)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "fabric_id is not a uuid")
		return nil, false
	}
	return &id, true
}

type blocklistsPage = httpx.Page[dbq.DnsBlocklist]

func (h *Handler) listBlocklists(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	fid, ok := fabricIDFromQuery(w, q)
	if !ok {
		return
	}
	scopeIds, ok := scopedListFilter(r, "dns:blocklists:read")
	if !ok {
		httpx.JSON(w, http.StatusOK, httpx.EmptyPage[dbq.DnsBlocklist](limit, offset))
		return
	}
	params := dbq.ListDnsBlocklistsParams{Limit: limit, Offset: offset, FabricID: fid, ScopeFabricIds: scopeIds}
	items, err := h.Q.ListDnsBlocklists(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountDnsBlocklists(r.Context(), dbq.CountDnsBlocklistsParams{FabricID: fid, ScopeFabricIds: scopeIds})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, blocklistsPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

// blocklistEntriesPage is the wire shape for /api/v1/dns/blocklists/{id}/entries.
// Alias of httpx.Page[T] so empty-page short-circuits can use EmptyPage[T]
// (returns Items: [] not null, which finch's data.items.map() needs).
type blocklistEntriesPage = httpx.Page[dbq.DnsBlocklistEntry]

func (h *Handler) listBlocklistEntries(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	// Surface a missing parent blocklist as 404, mirroring the FastAPI handler.
	bl, err := h.Q.GetDnsBlocklist(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "blocklist not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	// Scope check on the parent blocklist's fabric (PR 60). A scoped
	// caller poking at a blocklist outside their scope gets an empty
	// page rather than the entry data. ListDnsBlocklistEntries itself
	// is keyed only by blocklist_id (no scope_fabric_ids in the SQL)
	// because the parent lookup above is sufficient — once we know
	// the caller can see the blocklist, they can see its entries.
	p, _ := auth.From(r.Context())
	if _, scoped := auth.ScopedFabricFilter(p, "dns:blocklists:read"); scoped {
		if err := auth.EnforceFabricScope(p, bl.FabricID, "dns:blocklists:read"); err != nil {
			limit, offset := httpx.PageBounds(r.URL.Query())
			httpx.JSON(w, http.StatusOK, httpx.EmptyPage[dbq.DnsBlocklistEntry](limit, offset))
			return
		}
	}
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	items, err := h.Q.ListDnsBlocklistEntries(r.Context(), dbq.ListDnsBlocklistEntriesParams{
		Limit: limit, Offset: offset, BlocklistID: id,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountDnsBlocklistEntries(r.Context(), id)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, blocklistEntriesPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

type viewsPage = httpx.Page[dbq.DnsView]

func (h *Handler) listViews(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	fid, ok := fabricIDFromQuery(w, q)
	if !ok {
		return
	}
	scopeIds, ok := scopedListFilter(r, "dns:views:read")
	if !ok {
		httpx.JSON(w, http.StatusOK, httpx.EmptyPage[dbq.DnsView](limit, offset))
		return
	}
	items, err := h.Q.ListDnsViews(r.Context(), dbq.ListDnsViewsParams{Limit: limit, Offset: offset, FabricID: fid, ScopeFabricIds: scopeIds})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountDnsViews(r.Context(), dbq.CountDnsViewsParams{FabricID: fid, ScopeFabricIds: scopeIds})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, viewsPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

type healthChecksPage = httpx.Page[dbq.DnsHealthCheck]

func (h *Handler) listHealthChecks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	fid, ok := fabricIDFromQuery(w, q)
	if !ok {
		return
	}
	scopeIds, ok := scopedListFilter(r, "dns:health-checks:read")
	if !ok {
		httpx.JSON(w, http.StatusOK, httpx.EmptyPage[dbq.DnsHealthCheck](limit, offset))
		return
	}
	items, err := h.Q.ListDnsHealthChecks(r.Context(), dbq.ListDnsHealthChecksParams{Limit: limit, Offset: offset, FabricID: fid, ScopeFabricIds: scopeIds})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountDnsHealthChecks(r.Context(), dbq.CountDnsHealthChecksParams{FabricID: fid, ScopeFabricIds: scopeIds})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, healthChecksPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

type bgpPeersPage = httpx.Page[dbq.BgpPeer]

func (h *Handler) listBgpPeers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	// PR 63 — bgp_peers.site_id is NOT NULL (no enterprise-default
	// semantic), so short-circuit on empty allowed set.
	p, _ := auth.From(r.Context())
	scopeSiteIds, scoped, err := auth.ScopedSiteFilter(r.Context(), h.Q, p, "dns:bgp-peers:read")
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if scoped && len(scopeSiteIds) == 0 {
		httpx.JSON(w, http.StatusOK, httpx.EmptyPage[dbq.BgpPeer](limit, offset))
		return
	}
	params := dbq.ListBgpPeersParams{Limit: limit, Offset: offset, ScopeSiteIds: scopeSiteIds}
	if v := q.Get("site_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "site_id is not a uuid")
			return
		}
		params.SiteID = &id
	}
	items, err := h.Q.ListBgpPeers(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountBgpPeers(r.Context(), dbq.CountBgpPeersParams{SiteID: params.SiteID, ScopeSiteIds: scopeSiteIds})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, bgpPeersPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

type anycastBindingsPage = httpx.Page[dbq.AnycastBgpBinding]

func (h *Handler) listAnycastBindings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	scopeIds, ok := scopedListFilter(r, "dns:anycast-bindings:read")
	if !ok {
		httpx.JSON(w, http.StatusOK, httpx.EmptyPage[dbq.AnycastBgpBinding](limit, offset))
		return
	}
	params := dbq.ListAnycastBindingsParams{Limit: limit, Offset: offset, ScopeFabricIds: scopeIds}
	for _, f := range []struct {
		key string
		dst **uuid.UUID
	}{
		{"dns_server_id", &params.DnsServerID},
		{"bgp_peer_id", &params.BgpPeerID},
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
	items, err := h.Q.ListAnycastBindings(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountAnycastBindings(r.Context(), dbq.CountAnycastBindingsParams{
		DnsServerID: params.DnsServerID, BgpPeerID: params.BgpPeerID,
		ScopeFabricIds: scopeIds,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, anycastBindingsPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

type serversPage = httpx.Page[dbq.DnsServer]

func (h *Handler) listServers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	scopeIds, ok := scopedListFilter(r, "dns:servers:read")
	if !ok {
		httpx.JSON(w, http.StatusOK, httpx.EmptyPage[dbq.DnsServer](limit, offset))
		return
	}
	params := dbq.ListDnsServersParams{Limit: limit, Offset: offset, Role: strPtr(q.Get("role")), ScopeFabricIds: scopeIds}
	for _, f := range []struct {
		key string
		dst **uuid.UUID
	}{
		{"site_id", &params.SiteID},
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
	items, err := h.Q.ListDnsServers(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountDnsServers(r.Context(), dbq.CountDnsServersParams{
		SiteID: params.SiteID, FabricID: params.FabricID, Role: params.Role,
		ScopeFabricIds: scopeIds,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, serversPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

func (h *Handler) getServer(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	obj, err := h.Q.GetDnsServer(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "dns server not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, obj)
}

type anycastGroupsPage = httpx.Page[dbq.AnycastGroup]

func (h *Handler) listAnycastGroups(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	scopeIds, ok := scopedListFilter(r, "dns:anycast-groups:read")
	if !ok {
		httpx.JSON(w, http.StatusOK, httpx.EmptyPage[dbq.AnycastGroup](limit, offset))
		return
	}
	params := dbq.ListAnycastGroupsParams{Limit: limit, Offset: offset, Service: strPtr(q.Get("service")), ScopeFabricIds: scopeIds}
	if v := q.Get("fabric_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "fabric_id is not a uuid")
			return
		}
		params.FabricID = &id
	}
	items, err := h.Q.ListAnycastGroups(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountAnycastGroups(r.Context(), dbq.CountAnycastGroupsParams{
		FabricID: params.FabricID, Service: params.Service,
		ScopeFabricIds: scopeIds,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, anycastGroupsPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

type forwardersPage = httpx.Page[dbq.DnsForwarder]

func (h *Handler) listForwarders(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	scopeIds, ok := scopedListFilter(r, "dns:forwarders:read")
	if !ok {
		httpx.JSON(w, http.StatusOK, httpx.EmptyPage[dbq.DnsForwarder](limit, offset))
		return
	}
	params := dbq.ListDnsForwardersParams{Limit: limit, Offset: offset, ScopeFabricIds: scopeIds}
	if v := q.Get("fabric_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "fabric_id is not a uuid")
			return
		}
		params.FabricID = &id
	}
	items, err := h.Q.ListDnsForwarders(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountDnsForwarders(r.Context(), dbq.CountDnsForwardersParams{FabricID: params.FabricID, ScopeFabricIds: scopeIds})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, forwardersPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

type catalogZonesPage = httpx.Page[dbq.DnsCatalogZone]

func (h *Handler) listCatalogZones(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	scopeIds, ok := scopedListFilter(r, "dns:catalog-zones:read")
	if !ok {
		httpx.JSON(w, http.StatusOK, httpx.EmptyPage[dbq.DnsCatalogZone](limit, offset))
		return
	}
	params := dbq.ListDnsCatalogZonesParams{Limit: limit, Offset: offset, ScopeFabricIds: scopeIds}
	if v := q.Get("fabric_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "fabric_id is not a uuid")
			return
		}
		params.FabricID = &id
	}
	items, err := h.Q.ListDnsCatalogZones(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountDnsCatalogZones(r.Context(), dbq.CountDnsCatalogZonesParams{FabricID: params.FabricID, ScopeFabricIds: scopeIds})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, catalogZonesPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

type zonesPage = httpx.Page[dbq.DnsZone]

func (h *Handler) listZones(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	scopeIds, ok := scopedListFilter(r, "dns:zones:read")
	if !ok {
		httpx.JSON(w, http.StatusOK, httpx.EmptyPage[dbq.DnsZone](limit, offset))
		return
	}
	params := dbq.ListDnsZonesParams{Limit: limit, Offset: offset, Kind: strPtr(q.Get("kind")), ScopeFabricIds: scopeIds}
	for _, f := range []struct {
		key string
		dst **uuid.UUID
	}{
		{"fabric_id", &params.FabricID},
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
	items, err := h.Q.ListDnsZones(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountDnsZones(r.Context(), dbq.CountDnsZonesParams{
		FabricID: params.FabricID, SiteID: params.SiteID, Kind: params.Kind,
		ScopeFabricIds: scopeIds,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, zonesPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

func (h *Handler) getZone(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	z, err := h.Q.GetDnsZone(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "zone not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, z)
}

type recordsPage = httpx.Page[dbq.DnsRecord]

func (h *Handler) listRecords(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	scopeIds, ok := scopedListFilter(r, "dns:records:read")
	if !ok {
		httpx.JSON(w, http.StatusOK, httpx.EmptyPage[dbq.DnsRecord](limit, offset))
		return
	}
	params := dbq.ListDnsRecordsParams{
		Limit: limit, Offset: offset,
		Type: strPtr(q.Get("type")), Source: strPtr(q.Get("source")),
		ScopeFabricIds: scopeIds,
	}
	if v := q.Get("zone_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "zone_id is not a uuid")
			return
		}
		params.ZoneID = &id
	}
	items, err := h.Q.ListDnsRecords(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountDnsRecords(r.Context(), dbq.CountDnsRecordsParams{
		ZoneID: params.ZoneID, Type: params.Type, Source: params.Source,
		ScopeFabricIds: scopeIds,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, recordsPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func first(q map[string][]string, key string) string {
	if vs := q[key]; len(vs) > 0 {
		return vs[0]
	}
	return ""
}
