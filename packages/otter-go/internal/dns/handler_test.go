package dns

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

type fakeQ struct {
	lastZone    dbq.ListDnsZonesParams
	lastRec     dbq.ListDnsRecordsParams
	lastServer  dbq.ListDnsServersParams
	lastAnycast dbq.ListAnycastGroupsParams
	lastFwd     dbq.ListDnsForwardersParams
	lastCatalog dbq.ListDnsCatalogZonesParams
	lastBL      dbq.ListDnsBlocklistsParams
	lastBLE     dbq.ListDnsBlocklistEntriesParams
	lastView    dbq.ListDnsViewsParams
	lastHC      dbq.ListDnsHealthChecksParams
	lastPeer    dbq.ListBgpPeersParams
	lastBind    dbq.ListAnycastBindingsParams
	blGetErr    error
}

func (f *fakeQ) ListDnsZones(_ context.Context, a dbq.ListDnsZonesParams) ([]dbq.DnsZone, error) {
	f.lastZone = a
	return nil, nil
}
func (f *fakeQ) CountDnsZones(_ context.Context, _ dbq.CountDnsZonesParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) GetDnsZone(_ context.Context, _ uuid.UUID) (dbq.DnsZone, error) {
	return dbq.DnsZone{}, pgx.ErrNoRows
}
func (f *fakeQ) ListDnsRecords(_ context.Context, a dbq.ListDnsRecordsParams) ([]dbq.DnsRecord, error) {
	f.lastRec = a
	return nil, nil
}
func (f *fakeQ) CountDnsRecords(_ context.Context, _ dbq.CountDnsRecordsParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) ListDnsServers(_ context.Context, a dbq.ListDnsServersParams) ([]dbq.DnsServer, error) {
	f.lastServer = a
	return nil, nil
}
func (f *fakeQ) CountDnsServers(_ context.Context, _ dbq.CountDnsServersParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) GetDnsServer(_ context.Context, _ uuid.UUID) (dbq.DnsServer, error) {
	return dbq.DnsServer{}, pgx.ErrNoRows
}
func (f *fakeQ) ListAnycastGroups(_ context.Context, a dbq.ListAnycastGroupsParams) ([]dbq.AnycastGroup, error) {
	f.lastAnycast = a
	return nil, nil
}
func (f *fakeQ) CountAnycastGroups(_ context.Context, _ dbq.CountAnycastGroupsParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) ListDnsForwarders(_ context.Context, a dbq.ListDnsForwardersParams) ([]dbq.DnsForwarder, error) {
	f.lastFwd = a
	return nil, nil
}
func (f *fakeQ) CountDnsForwarders(_ context.Context, _ dbq.CountDnsForwardersParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) ListDnsCatalogZones(_ context.Context, a dbq.ListDnsCatalogZonesParams) ([]dbq.DnsCatalogZone, error) {
	f.lastCatalog = a
	return nil, nil
}
func (f *fakeQ) CountDnsCatalogZones(_ context.Context, _ dbq.CountDnsCatalogZonesParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) ListDnsBlocklists(_ context.Context, a dbq.ListDnsBlocklistsParams) ([]dbq.DnsBlocklist, error) {
	f.lastBL = a
	return nil, nil
}
func (f *fakeQ) CountDnsBlocklists(_ context.Context, _ dbq.CountDnsBlocklistsParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) GetDnsBlocklist(_ context.Context, _ uuid.UUID) (dbq.DnsBlocklist, error) {
	if f.blGetErr != nil {
		return dbq.DnsBlocklist{}, f.blGetErr
	}
	return dbq.DnsBlocklist{}, nil
}
func (f *fakeQ) ListDnsBlocklistEntries(_ context.Context, a dbq.ListDnsBlocklistEntriesParams) ([]dbq.DnsBlocklistEntry, error) {
	f.lastBLE = a
	return nil, nil
}
func (f *fakeQ) CountDnsBlocklistEntries(_ context.Context, _ uuid.UUID) (int64, error) {
	return 0, nil
}
func (f *fakeQ) ListDnsViews(_ context.Context, a dbq.ListDnsViewsParams) ([]dbq.DnsView, error) {
	f.lastView = a
	return nil, nil
}
func (f *fakeQ) CountDnsViews(_ context.Context, _ dbq.CountDnsViewsParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) ListDnsHealthChecks(_ context.Context, a dbq.ListDnsHealthChecksParams) ([]dbq.DnsHealthCheck, error) {
	f.lastHC = a
	return nil, nil
}
func (f *fakeQ) CountDnsHealthChecks(_ context.Context, _ dbq.CountDnsHealthChecksParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) ListBgpPeers(_ context.Context, a dbq.ListBgpPeersParams) ([]dbq.BgpPeer, error) {
	f.lastPeer = a
	return nil, nil
}
func (f *fakeQ) CountBgpPeers(_ context.Context, _ dbq.CountBgpPeersParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) ListAnycastBindings(_ context.Context, a dbq.ListAnycastBindingsParams) ([]dbq.AnycastBgpBinding, error) {
	f.lastBind = a
	return nil, nil
}
func (f *fakeQ) CountAnycastBindings(_ context.Context, _ dbq.CountAnycastBindingsParams) (int64, error) {
	return 0, nil
}

// ---- Mutation stubs (PR 43) ----

func (f *fakeQ) CreateDnsZone(_ context.Context, a dbq.CreateDnsZoneParams) (dbq.DnsZone, error) {
	return dbq.DnsZone{ID: uuid.New(), Name: a.Name, Kind: a.Kind, FabricID: a.FabricID}, nil
}
func (f *fakeQ) UpdateDnsZone(_ context.Context, a dbq.UpdateDnsZoneParams) (dbq.DnsZone, error) {
	return dbq.DnsZone{ID: a.ID}, nil
}
func (f *fakeQ) DeleteDnsZone(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreateDnsRecord(_ context.Context, a dbq.CreateDnsRecordParams) (dbq.DnsRecord, error) {
	return dbq.DnsRecord{ID: uuid.New(), ZoneID: a.ZoneID, Name: a.Name, Type: a.Type, Data: a.Data}, nil
}
func (f *fakeQ) UpdateDnsRecord(_ context.Context, a dbq.UpdateDnsRecordParams) (dbq.DnsRecord, error) {
	return dbq.DnsRecord{ID: a.ID}, nil
}
func (f *fakeQ) DeleteDnsRecord(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreateDnsServerRow(_ context.Context, a dbq.CreateDnsServerRowParams) (dbq.DnsServer, error) {
	return dbq.DnsServer{ID: uuid.New(), Name: a.Name, SiteID: a.SiteID, FabricID: a.FabricID, Role: a.Role}, nil
}
func (f *fakeQ) UpdateDnsServerRow(_ context.Context, a dbq.UpdateDnsServerRowParams) (dbq.DnsServer, error) {
	return dbq.DnsServer{ID: a.ID}, nil
}
func (f *fakeQ) DeleteDnsServerRow(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreateAnycastGroup(_ context.Context, a dbq.CreateAnycastGroupParams) (dbq.AnycastGroup, error) {
	return dbq.AnycastGroup{ID: uuid.New(), Name: a.Name, FabricID: a.FabricID, Service: a.Service}, nil
}
func (f *fakeQ) UpdateAnycastGroup(_ context.Context, a dbq.UpdateAnycastGroupParams) (dbq.AnycastGroup, error) {
	return dbq.AnycastGroup{ID: a.ID}, nil
}
func (f *fakeQ) DeleteAnycastGroup(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreateDnsForwarder(_ context.Context, a dbq.CreateDnsForwarderParams) (dbq.DnsForwarder, error) {
	return dbq.DnsForwarder{ID: uuid.New(), Name: a.Name, FabricID: a.FabricID, ZonePattern: a.ZonePattern, Upstreams: a.Upstreams}, nil
}
func (f *fakeQ) UpdateDnsForwarder(_ context.Context, a dbq.UpdateDnsForwarderParams) (dbq.DnsForwarder, error) {
	return dbq.DnsForwarder{ID: a.ID}, nil
}
func (f *fakeQ) DeleteDnsForwarder(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreateDnsCatalogZone(_ context.Context, a dbq.CreateDnsCatalogZoneParams) (dbq.DnsCatalogZone, error) {
	return dbq.DnsCatalogZone{ID: uuid.New(), FabricID: a.FabricID, Name: a.Name, Enabled: a.Enabled}, nil
}
func (f *fakeQ) UpdateDnsCatalogZone(_ context.Context, a dbq.UpdateDnsCatalogZoneParams) (dbq.DnsCatalogZone, error) {
	return dbq.DnsCatalogZone{ID: a.ID}, nil
}
func (f *fakeQ) DeleteDnsCatalogZone(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreateDnsBlocklist(_ context.Context, a dbq.CreateDnsBlocklistParams) (dbq.DnsBlocklist, error) {
	return dbq.DnsBlocklist{ID: uuid.New(), Name: a.Name, FabricID: a.FabricID, Action: a.Action}, nil
}
func (f *fakeQ) UpdateDnsBlocklist(_ context.Context, a dbq.UpdateDnsBlocklistParams) (dbq.DnsBlocklist, error) {
	return dbq.DnsBlocklist{ID: a.ID}, nil
}
func (f *fakeQ) DeleteDnsBlocklist(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreateDnsBlocklistEntry(_ context.Context, a dbq.CreateDnsBlocklistEntryParams) (dbq.DnsBlocklistEntry, error) {
	return dbq.DnsBlocklistEntry{ID: uuid.New(), BlocklistID: a.BlocklistID, Pattern: a.Pattern}, nil
}
func (f *fakeQ) DeleteDnsBlocklistEntry(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreateDnsView(_ context.Context, a dbq.CreateDnsViewParams) (dbq.DnsView, error) {
	return dbq.DnsView{ID: uuid.New(), Name: a.Name, FabricID: a.FabricID, MatchCidrs: a.MatchCidrs, Priority: a.Priority}, nil
}
func (f *fakeQ) UpdateDnsView(_ context.Context, a dbq.UpdateDnsViewParams) (dbq.DnsView, error) {
	return dbq.DnsView{ID: a.ID}, nil
}
func (f *fakeQ) DeleteDnsView(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreateDnsHealthCheck(_ context.Context, a dbq.CreateDnsHealthCheckParams) (dbq.DnsHealthCheck, error) {
	return dbq.DnsHealthCheck{ID: uuid.New(), Name: a.Name, FabricID: a.FabricID, TargetIP: a.TargetIP, Protocol: a.Protocol}, nil
}
func (f *fakeQ) UpdateDnsHealthCheck(_ context.Context, a dbq.UpdateDnsHealthCheckParams) (dbq.DnsHealthCheck, error) {
	return dbq.DnsHealthCheck{ID: a.ID}, nil
}
func (f *fakeQ) DeleteDnsHealthCheck(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreateBgpPeer(_ context.Context, a dbq.CreateBgpPeerParams) (dbq.BgpPeer, error) {
	return dbq.BgpPeer{ID: uuid.New(), Name: a.Name, SiteID: a.SiteID}, nil
}
func (f *fakeQ) UpdateBgpPeer(_ context.Context, a dbq.UpdateBgpPeerParams) (dbq.BgpPeer, error) {
	return dbq.BgpPeer{ID: a.ID}, nil
}
func (f *fakeQ) DeleteBgpPeer(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreateAnycastBinding(_ context.Context, a dbq.CreateAnycastBindingParams) (dbq.AnycastBgpBinding, error) {
	return dbq.AnycastBgpBinding{ID: uuid.New(), DnsServerID: a.DnsServerID, BgpPeerID: a.BgpPeerID}, nil
}
func (f *fakeQ) DeleteAnycastBinding(_ context.Context, _ uuid.UUID) error { return nil }

func mount(f *fakeQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}
func do(t *testing.T, h http.Handler, p string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
	return rec
}

func TestListZones_AllFilters(t *testing.T) {
	fid, sid := uuid.New(), uuid.New()
	f := &fakeQ{}
	rec := do(t, mount(f), "/dns/zones?fabric_id="+fid.String()+"&site_id="+sid.String()+"&kind=site")
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if f.lastZone.FabricID == nil || *f.lastZone.FabricID != fid {
		t.Error("fabric_id")
	}
	if f.lastZone.SiteID == nil || *f.lastZone.SiteID != sid {
		t.Error("site_id")
	}
	if f.lastZone.Kind == nil || *f.lastZone.Kind != "site" {
		t.Error("kind")
	}
}

func TestGetZone_NotFound(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/dns/zones/"+uuid.New().String())
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}

func TestListRecords_AllFilters(t *testing.T) {
	zid := uuid.New()
	f := &fakeQ{}
	rec := do(t, mount(f), "/dns/records?zone_id="+zid.String()+"&type=A&source=manual")
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if f.lastRec.ZoneID == nil || *f.lastRec.ZoneID != zid {
		t.Error("zone_id")
	}
	if f.lastRec.Type == nil || *f.lastRec.Type != "A" {
		t.Error("type")
	}
	if f.lastRec.Source == nil || *f.lastRec.Source != "manual" {
		t.Error("source")
	}
}

func TestListZones_PageSizeAlias(t *testing.T) {
	f := &fakeQ{}
	rec := do(t, mount(f), "/dns/zones?page_size=200")
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if f.lastZone.Limit != 200 {
		t.Errorf("page_size not honored: %d", f.lastZone.Limit)
	}
}

func TestListServers_AllFilters(t *testing.T) {
	sid, fid := uuid.New(), uuid.New()
	f := &fakeQ{}
	do(t, mount(f), "/dns/servers?site_id="+sid.String()+"&fabric_id="+fid.String()+"&role=auth")
	if f.lastServer.SiteID == nil || *f.lastServer.SiteID != sid {
		t.Error("site_id")
	}
	if f.lastServer.FabricID == nil || *f.lastServer.FabricID != fid {
		t.Error("fabric_id")
	}
	if f.lastServer.Role == nil || *f.lastServer.Role != "auth" {
		t.Error("role")
	}
}

func TestGetServer_NotFound(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/dns/servers/"+uuid.New().String())
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}

func TestListAnycastGroups_AllFilters(t *testing.T) {
	fid := uuid.New()
	f := &fakeQ{}
	do(t, mount(f), "/dns/anycast-groups?fabric_id="+fid.String()+"&service=dns_recursive")
	if f.lastAnycast.FabricID == nil || *f.lastAnycast.FabricID != fid {
		t.Error("fabric_id")
	}
	if f.lastAnycast.Service == nil || *f.lastAnycast.Service != "dns_recursive" {
		t.Error("service")
	}
}

func TestListForwarders_FabricFilter(t *testing.T) {
	fid := uuid.New()
	f := &fakeQ{}
	do(t, mount(f), "/dns/forwarders?fabric_id="+fid.String())
	if f.lastFwd.FabricID == nil || *f.lastFwd.FabricID != fid {
		t.Error("fabric_id")
	}
}

func TestListCatalogZones_FabricFilter(t *testing.T) {
	fid := uuid.New()
	f := &fakeQ{}
	do(t, mount(f), "/dns/catalog-zones?fabric_id="+fid.String())
	if f.lastCatalog.FabricID == nil || *f.lastCatalog.FabricID != fid {
		t.Error("fabric_id")
	}
}

func TestListBlocklists_FabricFilter(t *testing.T) {
	fid := uuid.New()
	f := &fakeQ{}
	do(t, mount(f), "/dns/blocklists?fabric_id="+fid.String())
	if f.lastBL.FabricID == nil || *f.lastBL.FabricID != fid {
		t.Error("fabric_id")
	}
}

func TestListBlocklistEntries_PassesParent(t *testing.T) {
	bid := uuid.New()
	f := &fakeQ{}
	rec := do(t, mount(f), "/dns/blocklists/"+bid.String()+"/entries")
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if f.lastBLE.BlocklistID != bid {
		t.Error("blocklist_id")
	}
}

func TestListBlocklistEntries_ParentNotFound(t *testing.T) {
	f := &fakeQ{blGetErr: pgx.ErrNoRows}
	rec := do(t, mount(f), "/dns/blocklists/"+uuid.New().String()+"/entries")
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}

func TestListViews_FabricFilter(t *testing.T) {
	fid := uuid.New()
	f := &fakeQ{}
	do(t, mount(f), "/dns/views?fabric_id="+fid.String())
	if f.lastView.FabricID == nil || *f.lastView.FabricID != fid {
		t.Error("fabric_id")
	}
}

func TestListHealthChecks_FabricFilter(t *testing.T) {
	fid := uuid.New()
	f := &fakeQ{}
	do(t, mount(f), "/dns/health-checks?fabric_id="+fid.String())
	if f.lastHC.FabricID == nil || *f.lastHC.FabricID != fid {
		t.Error("fabric_id")
	}
}

func TestListBgpPeers_SiteFilter(t *testing.T) {
	sid := uuid.New()
	f := &fakeQ{}
	do(t, mount(f), "/dns/bgp-peers?site_id="+sid.String())
	if f.lastPeer.SiteID == nil || *f.lastPeer.SiteID != sid {
		t.Error("site_id")
	}
}

func TestListAnycastBindings_BothFilters(t *testing.T) {
	dsid, pid := uuid.New(), uuid.New()
	f := &fakeQ{}
	do(t, mount(f), "/dns/anycast-bindings?dns_server_id="+dsid.String()+"&bgp_peer_id="+pid.String())
	if f.lastBind.DnsServerID == nil || *f.lastBind.DnsServerID != dsid {
		t.Error("dns_server_id")
	}
	if f.lastBind.BgpPeerID == nil || *f.lastBind.BgpPeerID != pid {
		t.Error("bgp_peer_id")
	}
}

func TestBadUUIDs(t *testing.T) {
	for _, p := range []string{
		"/dns/zones?fabric_id=x",
		"/dns/zones?site_id=x",
		"/dns/zones/x",
		"/dns/records?zone_id=x",
		"/dns/servers?site_id=x",
		"/dns/servers?fabric_id=x",
		"/dns/servers/x",
		"/dns/anycast-groups?fabric_id=x",
		"/dns/forwarders?fabric_id=x",
		"/dns/catalog-zones?fabric_id=x",
		"/dns/blocklists?fabric_id=x",
		"/dns/blocklists/x/entries",
		"/dns/views?fabric_id=x",
		"/dns/health-checks?fabric_id=x",
		"/dns/bgp-peers?site_id=x",
		"/dns/anycast-bindings?dns_server_id=x",
		"/dns/anycast-bindings?bgp_peer_id=x",
	} {
		rec := do(t, mount(&fakeQ{}), p)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d", p, rec.Code)
		}
	}
}
