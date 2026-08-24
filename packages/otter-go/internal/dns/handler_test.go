package dns

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
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
	// Bulk blocklist add (PR 23).
	bulkPatterns    []string
	existingPats    []string
	// Catalog disable-dnssec (PR 23).
	catalogZone     dbq.DnsCatalogZone
	catalogGetErr   error
	catalogKeyTags  []int32
	catalogDeleted  bool
	catalogSignedSet *bool
	// Call-ordering counters — read-tags MUST happen before delete-keys
	// before set-signed; otherwise the retired_key_tags audit metadata
	// is captured at the wrong moment.
	catalogReadTagsAt  int
	catalogDeletedAt   int
	catalogSignedSetAt int
	catalogCallCounter int
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
func (f *fakeQ) ListDnsServers(_ context.Context, a dbq.ListDnsServersParams) ([]dbq.ListDnsServersRow, error) {
	f.lastServer = a
	return nil, nil
}
func (f *fakeQ) CountDnsServers(_ context.Context, _ dbq.CountDnsServersParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) GetDnsServer(_ context.Context, _ uuid.UUID) (dbq.GetDnsServerRow, error) {
	return dbq.GetDnsServerRow{}, pgx.ErrNoRows
}
func (f *fakeQ) ListAnycastGroups(_ context.Context, a dbq.ListAnycastGroupsParams) ([]dbq.ListAnycastGroupsRow, error) {
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
func (f *fakeQ) ListDnsBlocklists(_ context.Context, a dbq.ListDnsBlocklistsParams) ([]dbq.ListDnsBlocklistsRow, error) {
	f.lastBL = a
	return nil, nil
}
func (f *fakeQ) CountDnsBlocklists(_ context.Context, _ dbq.CountDnsBlocklistsParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) GetDnsBlocklist(_ context.Context, _ uuid.UUID) (dbq.GetDnsBlocklistRow, error) {
	if f.blGetErr != nil {
		return dbq.GetDnsBlocklistRow{}, f.blGetErr
	}
	return dbq.GetDnsBlocklistRow{}, nil
}
func (f *fakeQ) ListDnsBlocklistPatternsByID(_ context.Context, _ uuid.UUID) ([]string, error) {
	return f.existingPats, nil
}
func (f *fakeQ) GetDnsCatalogZone(_ context.Context, _ uuid.UUID) (dbq.DnsCatalogZone, error) {
	if f.catalogGetErr != nil {
		return dbq.DnsCatalogZone{}, f.catalogGetErr
	}
	return f.catalogZone, nil
}
func (f *fakeQ) ListDnsKeyTagsByCatalog(_ context.Context, _ uuid.UUID) ([]int32, error) {
	f.catalogCallCounter++
	f.catalogReadTagsAt = f.catalogCallCounter
	return f.catalogKeyTags, nil
}
func (f *fakeQ) DeleteDnsKeysByCatalog(_ context.Context, _ uuid.UUID) error {
	f.catalogCallCounter++
	f.catalogDeletedAt = f.catalogCallCounter
	f.catalogDeleted = true
	return nil
}
func (f *fakeQ) SetDnsCatalogZoneSigned(_ context.Context, a dbq.SetDnsCatalogZoneSignedParams) error {
	f.catalogCallCounter++
	f.catalogSignedSetAt = f.catalogCallCounter
	v := a.Signed
	f.catalogSignedSet = &v
	return nil
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
func (f *fakeQ) ListDnsHealthChecks(_ context.Context, a dbq.ListDnsHealthChecksParams) ([]dbq.ListDnsHealthChecksRow, error) {
	f.lastHC = a
	return nil, nil
}
func (f *fakeQ) CountDnsHealthChecks(_ context.Context, _ dbq.CountDnsHealthChecksParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) ListBgpPeers(_ context.Context, a dbq.ListBgpPeersParams) ([]dbq.ListBgpPeersRow, error) {
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
func (f *fakeQ) SetDnsZoneFrozen(_ context.Context, a dbq.SetDnsZoneFrozenParams) (dbq.DnsZone, error) {
	return dbq.DnsZone{ID: a.ID, Frozen: a.Frozen}, nil
}
func (f *fakeQ) SetDnsZoneNsec3(_ context.Context, a dbq.SetDnsZoneNsec3Params) (dbq.DnsZone, error) {
	return dbq.DnsZone{ID: a.ID, Nsec3Salt: a.Salt, Nsec3Iterations: a.Iterations, Nsec3OptOut: a.OptOut, Signed: true}, nil
}
func (f *fakeQ) ListAllRecordsInZone(_ context.Context, _ uuid.UUID) ([]dbq.ListAllRecordsInZoneRow, error) {
	return nil, nil
}
func (f *fakeQ) SetDnsHealthCheckResult(_ context.Context, _ dbq.SetDnsHealthCheckResultParams) (int64, error) {
	return 1, nil
}
func (f *fakeQ) SetDnsServerRenderStatus(_ context.Context, _ dbq.SetDnsServerRenderStatusParams) (int64, error) {
	return 1, nil
}
func (f *fakeQ) CreateDnsServerMetricsSample(_ context.Context, a dbq.CreateDnsServerMetricsSampleParams) (dbq.DnsServerMetricsSample, error) {
	return dbq.DnsServerMetricsSample{
		ID: uuid.New(), ServerID: a.ServerID,
		IntervalSeconds: a.IntervalSeconds,
		Queries:         a.Queries, Nxdomain: a.Nxdomain,
		Servfail: a.Servfail, Noerror: a.Noerror,
		P50Ms: a.P50Ms, P95Ms: a.P95Ms, TopNames: a.TopNames,
	}, nil
}
func (f *fakeQ) ListDnsServerMetricsSamples(_ context.Context, _ dbq.ListDnsServerMetricsSamplesParams) ([]dbq.DnsServerMetricsSample, error) {
	return nil, nil
}
func (f *fakeQ) ListDnsKeysByZone(_ context.Context, _ uuid.UUID) ([]dbq.DnsKey, error) {
	return nil, nil
}
func (f *fakeQ) CreateDnsKey(_ context.Context, a dbq.CreateDnsKeyParams) (dbq.DnsKey, error) {
	return dbq.DnsKey{ID: uuid.New(), ZoneID: a.ZoneID, Role: a.Role,
		Algorithm: a.Algorithm, PrivatePem: a.PrivatePem,
		PublicKeyB64: a.PublicKeyB64, KeyTag: a.KeyTag}, nil
}
func (f *fakeQ) SetDnsZoneSigned(_ context.Context, _ dbq.SetDnsZoneSignedParams) (int64, error) {
	return 1, nil
}
func (f *fakeQ) ListActiveDnsKeysForZoneAndRole(_ context.Context, _ dbq.ListActiveDnsKeysForZoneAndRoleParams) ([]dbq.DnsKey, error) {
	return nil, nil
}
func (f *fakeQ) RetireDnsKey(_ context.Context, _ uuid.UUID) (int64, error) {
	return 1, nil
}
func (f *fakeQ) DeleteDnsKey(_ context.Context, _ uuid.UUID) (int64, error) {
	return 1, nil
}
func (f *fakeQ) RetireAllDnsKeysForZone(_ context.Context, _ uuid.UUID) (int64, error) {
	return 0, nil
}
func (f *fakeQ) DeleteAllDnsKeysForZone(_ context.Context, _ uuid.UUID) ([]dbq.DnsKey, error) {
	return nil, nil
}
func (f *fakeQ) GetDnsKey(_ context.Context, _ uuid.UUID) (dbq.DnsKey, error) {
	return dbq.DnsKey{}, pgx.ErrNoRows
}
func (f *fakeQ) TouchDnsZone(_ context.Context, _ uuid.UUID) (int64, error) {
	return 1, nil
}
func (f *fakeQ) DeleteManualRecordsInZone(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}
func (f *fakeQ) UpdateDnsZoneSoa(_ context.Context, _ dbq.UpdateDnsZoneSoaParams) error {
	return nil
}
func (f *fakeQ) ListReverseZonesForSite(_ context.Context, _ dbq.ListReverseZonesForSiteParams) ([]dbq.DnsZone, error) {
	return nil, nil
}
func (f *fakeQ) GetReverseZoneByName(_ context.Context, _ dbq.GetReverseZoneByNameParams) (dbq.DnsZone, error) {
	return dbq.DnsZone{}, pgx.ErrNoRows
}
func (f *fakeQ) CreateReverseZone(_ context.Context, _ dbq.CreateReverseZoneParams) (dbq.DnsZone, error) {
	return dbq.DnsZone{ID: uuid.New()}, nil
}
func (f *fakeQ) ListIPAddressesForSiteWithDnsName(_ context.Context, _ uuid.UUID) ([]dbq.ListIPAddressesForSiteWithDnsNameRow, error) {
	return nil, nil
}
func (f *fakeQ) DeleteIPAMRecordsInZones(_ context.Context, _ []uuid.UUID) error {
	return nil
}
func (f *fakeQ) CountIPAMRecordsInZones(_ context.Context, _ []uuid.UUID) (int64, error) {
	return 0, nil
}
func (f *fakeQ) CreateProjectedDnsRecord(_ context.Context, _ dbq.CreateProjectedDnsRecordParams) (uuid.UUID, error) {
	return uuid.New(), nil
}
func (f *fakeQ) ListDnsSamplesInWindow(_ context.Context, _ dbq.ListDnsSamplesInWindowParams) ([]dbq.DnsServerMetricsSample, error) {
	return nil, nil
}
func (f *fakeQ) ListDnsServersForDashboard(_ context.Context, _ *uuid.UUID) ([]dbq.ListDnsServersForDashboardRow, error) {
	return nil, nil
}
func (f *fakeQ) ListDnsZonesForDashboard(_ context.Context, _ *uuid.UUID) ([]dbq.ListDnsZonesForDashboardRow, error) {
	return nil, nil
}
func (f *fakeQ) CountAnycastGroupsForDashboard(_ context.Context, _ *uuid.UUID) (int64, error) {
	return 0, nil
}
func (f *fakeQ) CreateDnsRecord(_ context.Context, a dbq.CreateDnsRecordParams) (dbq.DnsRecord, error) {
	return dbq.DnsRecord{ID: uuid.New(), ZoneID: a.ZoneID, Name: a.Name, Type: a.Type, Data: a.Data}, nil
}
func (f *fakeQ) UpdateDnsRecord(_ context.Context, a dbq.UpdateDnsRecordParams) (dbq.DnsRecord, error) {
	return dbq.DnsRecord{ID: a.ID}, nil
}
func (f *fakeQ) DeleteDnsRecord(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreateDnsServerRow(_ context.Context, a dbq.CreateDnsServerRowParams) (dbq.CreateDnsServerRowRow, error) {
	return dbq.CreateDnsServerRowRow{ID: uuid.New(), Name: a.Name, SiteID: a.SiteID, FabricID: a.FabricID, Role: a.Role}, nil
}
func (f *fakeQ) UpdateDnsServerRow(_ context.Context, a dbq.UpdateDnsServerRowParams) (dbq.UpdateDnsServerRowRow, error) {
	return dbq.UpdateDnsServerRowRow{ID: a.ID}, nil
}
func (f *fakeQ) DeleteDnsServerRow(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreateAnycastGroup(_ context.Context, a dbq.CreateAnycastGroupParams) (dbq.CreateAnycastGroupRow, error) {
	return dbq.CreateAnycastGroupRow{ID: uuid.New(), Name: a.Name, FabricID: a.FabricID, Service: a.Service}, nil
}
func (f *fakeQ) UpdateAnycastGroup(_ context.Context, a dbq.UpdateAnycastGroupParams) (dbq.UpdateAnycastGroupRow, error) {
	return dbq.UpdateAnycastGroupRow{ID: a.ID}, nil
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
func (f *fakeQ) CreateDnsBlocklist(_ context.Context, a dbq.CreateDnsBlocklistParams) (dbq.CreateDnsBlocklistRow, error) {
	return dbq.CreateDnsBlocklistRow{ID: uuid.New(), Name: a.Name, FabricID: a.FabricID, Action: a.Action}, nil
}
func (f *fakeQ) UpdateDnsBlocklist(_ context.Context, a dbq.UpdateDnsBlocklistParams) (dbq.UpdateDnsBlocklistRow, error) {
	return dbq.UpdateDnsBlocklistRow{ID: a.ID}, nil
}
func (f *fakeQ) DeleteDnsBlocklist(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreateDnsBlocklistEntry(_ context.Context, a dbq.CreateDnsBlocklistEntryParams) (dbq.DnsBlocklistEntry, error) {
	f.bulkPatterns = append(f.bulkPatterns, a.Pattern)
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
func (f *fakeQ) CreateDnsHealthCheck(_ context.Context, a dbq.CreateDnsHealthCheckParams) (dbq.CreateDnsHealthCheckRow, error) {
	return dbq.CreateDnsHealthCheckRow{ID: uuid.New(), Name: a.Name, FabricID: a.FabricID, TargetIP: a.TargetIP, Protocol: a.Protocol}, nil
}
func (f *fakeQ) UpdateDnsHealthCheck(_ context.Context, a dbq.UpdateDnsHealthCheckParams) (dbq.UpdateDnsHealthCheckRow, error) {
	return dbq.UpdateDnsHealthCheckRow{ID: a.ID}, nil
}
func (f *fakeQ) DeleteDnsHealthCheck(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreateBgpPeer(_ context.Context, a dbq.CreateBgpPeerParams) (dbq.CreateBgpPeerRow, error) {
	return dbq.CreateBgpPeerRow{ID: uuid.New(), Name: a.Name, SiteID: a.SiteID}, nil
}
func (f *fakeQ) UpdateBgpPeer(_ context.Context, a dbq.UpdateBgpPeerParams) (dbq.UpdateBgpPeerRow, error) {
	return dbq.UpdateBgpPeerRow{ID: a.ID}, nil
}
func (f *fakeQ) DeleteBgpPeer(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreateAnycastBinding(_ context.Context, a dbq.CreateAnycastBindingParams) (dbq.AnycastBgpBinding, error) {
	return dbq.AnycastBgpBinding{ID: uuid.New(), DnsServerID: a.DnsServerID, BgpPeerID: a.BgpPeerID}, nil
}
func (f *fakeQ) DeleteAnycastBinding(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) GetZoneFrozenByRecord(_ context.Context, _ uuid.UUID) (dbq.GetZoneFrozenByRecordRow, error) {
	return dbq.GetZoneFrozenByRecordRow{ZoneID: uuid.New(), Frozen: false}, nil
}

// ABAC parent-fabric lookups (PR 57). Tests that don't care about scope
// let these return uuid.Nil (treated as "no fabric to enforce" by
// EnforceFabricScope, so global behavior).
func (f *fakeQ) GetDnsZoneFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *fakeQ) GetDnsRecordFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *fakeQ) GetDnsServerFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *fakeQ) GetAnycastGroupFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *fakeQ) GetDnsForwarderFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *fakeQ) GetDnsCatalogZoneFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *fakeQ) GetDnsBlocklistFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	if f.blGetErr != nil {
		return uuid.Nil, f.blGetErr
	}
	return uuid.Nil, nil
}
func (f *fakeQ) GetDnsBlocklistEntryFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *fakeQ) GetDnsViewFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *fakeQ) GetDnsHealthCheckFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, nil
}

// PR 58 — BGP peer site lookup + anycast binding fabric lookup. Tests
// that don't care about scope let these return uuid.Nil (treated as
// "no fabric/site to enforce", so global behavior).
func (f *fakeQ) GetBgpPeerSiteID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *fakeQ) GetAnycastBindingDnsServerFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *fakeQ) GetSiteRegionID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *fakeQ) GetSiteOrganizationID(_ context.Context, _ uuid.UUID) (*uuid.UUID, error) {
	return nil, nil
}
func (f *fakeQ) ListSiteGroupIDsForSite(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

// PR 63 — site-scope expansion. Default returns nil (no expansion).
func (f *fakeQ) ListSiteIDsForExpansion(_ context.Context, _ dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error) {
	return nil, nil
}

func mount(f *fakeQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}

// fakeAudit captures audit.Record calls so PR 23's bulk_add and
// disable-dnssec parity (action/target_type/target_id/metadata)
// can be asserted directly rather than reviewed-only.
type fakeAudit struct {
	calls []dbq.InsertAuditLogParams
}

func (a *fakeAudit) InsertAuditLog(_ context.Context, p dbq.InsertAuditLogParams) error {
	a.calls = append(a.calls, p)
	return nil
}

func mountWithAudit(f *fakeQ, a *fakeAudit) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f, Audit: a}).Mount(r)
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

// ===== PR 23: bulk blocklist entries + catalog disable-dnssec =====

func postJSON(t *testing.T, h http.Handler, path, body string, caps ...string) *httptest.ResponseRecorder {
	t.Helper()
	if len(caps) == 0 {
		caps = []string{"*"}
	}
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{Capabilities: caps}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Python normalizes patterns (trim+lowercase, drop empties, dedupe);
// Go must match so threat-feed imports stay byte-equivalent.
func TestBulkAddBlocklistEntries_NormalizesAndDedups(t *testing.T) {
	f := &fakeQ{existingPats: []string{"baz.example."}}
	id := uuid.New().String()
	body := `{"patterns": ["  FOO.example. ", "foo.example.", "bar.example.", "baz.example.", "", "  "]}`
	rec := postJSON(t, mount(f), "/dns/blocklists/"+id+"/entries/bulk", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	var out struct{ Added, Skipped int }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	// Incoming deduped to 3 distinct (foo, bar, baz). baz already
	// exists. Added = 2 (foo, bar), Skipped = 1 (baz).
	if out.Added != 2 || out.Skipped != 1 {
		t.Errorf("added/skipped: got %d/%d, want 2/1", out.Added, out.Skipped)
	}
	// Sort to match Python's `sorted(incoming - existing_set)` —
	// pattern insert order is alphabetical.
	if len(f.bulkPatterns) != 2 || f.bulkPatterns[0] != "bar.example." || f.bulkPatterns[1] != "foo.example." {
		t.Errorf("insert order/values wrong: %v", f.bulkPatterns)
	}
}

func TestBulkAddBlocklistEntries_EmptyAfterNormalizeNoOps(t *testing.T) {
	f := &fakeQ{}
	id := uuid.New().String()
	rec := postJSON(t, mount(f), "/dns/blocklists/"+id+"/entries/bulk", `{"patterns": ["", "   "]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	if len(f.bulkPatterns) != 0 {
		t.Errorf("should NOT insert anything when normalized input is empty; got %v", f.bulkPatterns)
	}
}

// patterns:[] is a caller bug per Python's pydantic min_length=1
// — Go now matches with 400, not silent {0,0}.
func TestBulkAddBlocklistEntries_EmptyListRejected(t *testing.T) {
	f := &fakeQ{}
	id := uuid.New().String()
	rec := postJSON(t, mount(f), "/dns/blocklists/"+id+"/entries/bulk", `{"patterns": []}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400 (Python pydantic min_length=1 parity)", rec.Code)
	}
}

// Incoming entirely-duplicate: ListPatterns covers all, toAdd is
// empty, CreateDnsBlocklistEntry is never called, audit still fires
// with {added:0, skipped:N}. Guards against a diffPatterns regression
// that mishandles set semantics (e.g., returning `existing` or
// `incoming` instead of `incoming - existing`).
func TestBulkAddBlocklistEntries_AllDuplicatesAuditOnly(t *testing.T) {
	f := &fakeQ{existingPats: []string{"foo.example.", "bar.example."}}
	id := uuid.New().String()
	body := `{"patterns": ["foo.example.", "bar.example."]}`
	rec := postJSON(t, mount(f), "/dns/blocklists/"+id+"/entries/bulk", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if len(f.bulkPatterns) != 0 {
		t.Errorf("CreateDnsBlocklistEntry must NOT be called when every pattern already exists; got %v", f.bulkPatterns)
	}
	var out struct{ Added, Skipped int }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Added != 0 || out.Skipped != 2 {
		t.Errorf("added/skipped: got %d/%d, want 0/2", out.Added, out.Skipped)
	}
}

func TestBulkAddBlocklistEntries_BlocklistNotFound(t *testing.T) {
	f := &fakeQ{blGetErr: pgx.ErrNoRows}
	id := uuid.New().String()
	rec := postJSON(t, mount(f), "/dns/blocklists/"+id+"/entries/bulk", `{"patterns":["x"]}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404 blocklist not found", rec.Code)
	}
}

func TestBulkAddBlocklistEntries_RequiresCap(t *testing.T) {
	f := &fakeQ{}
	id := uuid.New().String()
	rec := postJSON(t, mount(f), "/dns/blocklists/"+id+"/entries/bulk", `{"patterns":["x"]}`, "unrelated")
	if rec.Code != http.StatusForbidden {
		t.Errorf("got %d, want 403 without dns:blocklists:update", rec.Code)
	}
}

// disable-dnssec on an already-unsigned catalog is a no-op 204 with
// no audit + no key DELETE — mirror Python's `if not catalog.signed: return`.
func TestDisableCatalogDnssec_AlreadyUnsignedNoOp(t *testing.T) {
	f := &fakeQ{catalogZone: dbq.DnsCatalogZone{ID: uuid.New(), FabricID: uuid.New(), Signed: false}}
	rec := postJSON(t, mount(f), "/dns/catalog-zones/"+f.catalogZone.ID.String()+"/disable-dnssec", "")
	if rec.Code != http.StatusNoContent {
		t.Errorf("got %d, want 204 no-op on unsigned catalog", rec.Code)
	}
	if f.catalogDeleted {
		t.Error("DeleteDnsKeysByCatalog called on unsigned catalog")
	}
	if f.catalogSignedSet != nil {
		t.Error("SetDnsCatalogZoneSigned called on unsigned catalog")
	}
}

func TestDisableCatalogDnssec_SignedRetiresKeys(t *testing.T) {
	cid := uuid.New()
	f := &fakeQ{
		catalogZone: dbq.DnsCatalogZone{
			ID: cid, FabricID: uuid.New(), Signed: true,
		},
		catalogKeyTags: []int32{12345, 54321},
	}
	a := &fakeAudit{}
	rec := postJSON(t, mountWithAudit(f, a), "/dns/catalog-zones/"+cid.String()+"/disable-dnssec", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	// Call ordering: read tags FIRST so the audit metadata captures
	// the real retired set; then DELETE keys; then flip signed. A
	// refactor that runs SetSigned before DELETE would corrupt the
	// catalog state under concurrent reads, and a refactor that
	// reads tags after DELETE would emit retired_key_tags=[].
	if !(f.catalogReadTagsAt < f.catalogDeletedAt && f.catalogDeletedAt < f.catalogSignedSetAt) {
		t.Errorf("call order wrong: readTags=%d delete=%d setSigned=%d (want read<delete<setSigned)",
			f.catalogReadTagsAt, f.catalogDeletedAt, f.catalogSignedSetAt)
	}
	if f.catalogSignedSet == nil || *f.catalogSignedSet != false {
		t.Errorf("signed flag not set to false: %v", f.catalogSignedSet)
	}
	// Audit shape parity with Python: action="dns_catalog_zone.disable_dnssec",
	// target_type="dns_catalog_zone", target_id=catalog_id,
	// metadata.retired_key_tags=[<ints>].
	if len(a.calls) != 1 {
		t.Fatalf("audit calls: got %d, want 1", len(a.calls))
	}
	c := a.calls[0]
	if c.Action != "dns_catalog_zone.disable_dnssec" {
		t.Errorf("audit action: got %q, want dns_catalog_zone.disable_dnssec", c.Action)
	}
	if c.TargetType == nil || *c.TargetType != "dns_catalog_zone" {
		t.Errorf("audit target_type: got %v", c.TargetType)
	}
	if c.TargetID == nil || *c.TargetID != cid.String() {
		t.Errorf("audit target_id: got %v, want %s", c.TargetID, cid)
	}
	// retired_key_tags is JSON-encoded into MetadataJson — decode and
	// compare. Python encodes as a JSON int array.
	var meta struct {
		RetiredKeyTags []int `json:"retired_key_tags"`
	}
	if err := json.Unmarshal(c.MetadataJson, &meta); err != nil {
		t.Fatalf("audit metadata not JSON: %v (raw=%s)", err, c.MetadataJson)
	}
	if len(meta.RetiredKeyTags) != 2 || meta.RetiredKeyTags[0] != 12345 || meta.RetiredKeyTags[1] != 54321 {
		t.Errorf("retired_key_tags: got %v, want [12345 54321]", meta.RetiredKeyTags)
	}
}

// Audit shape parity for bulk_add — note target_type is
// "dns_blocklist" (the parent) even though the action namespace is
// dns_blocklist_entry. Mirror of Python's audit.record call;
// downstream audit consumers index events by target_type so this
// asymmetry is deliberate.
func TestBulkAddBlocklistEntries_AuditShape(t *testing.T) {
	blID := uuid.New()
	f := &fakeQ{}
	// Stub the FabricID lookup to return a non-Nil fabric — the
	// default Nil/nil branch above will still satisfy enforceFabric
	// when the principal has "*", which is what postJSON uses.
	a := &fakeAudit{}
	body := `{"patterns": ["foo.example.", "bar.example."]}`
	rec := postJSON(t, mountWithAudit(f, a), "/dns/blocklists/"+blID.String()+"/entries/bulk", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if len(a.calls) != 1 {
		t.Fatalf("audit calls: got %d, want 1", len(a.calls))
	}
	c := a.calls[0]
	if c.Action != "dns_blocklist_entry.bulk_add" {
		t.Errorf("audit action: got %q", c.Action)
	}
	if c.TargetType == nil || *c.TargetType != "dns_blocklist" {
		t.Errorf("audit target_type: got %v (must be dns_blocklist not dns_blocklist_entry — see Python parity)", c.TargetType)
	}
	if c.TargetID == nil || *c.TargetID != blID.String() {
		t.Errorf("audit target_id: got %v, want %s", c.TargetID, blID)
	}
	var meta struct {
		Added, Skipped int
	}
	if err := json.Unmarshal(c.MetadataJson, &meta); err != nil {
		t.Fatalf("audit metadata: %v", err)
	}
	if meta.Added != 2 || meta.Skipped != 0 {
		t.Errorf("audit metadata added/skipped: got %d/%d, want 2/0", meta.Added, meta.Skipped)
	}
}

func TestDisableCatalogDnssec_NotFound(t *testing.T) {
	f := &fakeQ{catalogGetErr: pgx.ErrNoRows}
	id := uuid.New().String()
	rec := postJSON(t, mount(f), "/dns/catalog-zones/"+id+"/disable-dnssec", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404 catalog not found", rec.Code)
	}
}

func TestDisableCatalogDnssec_RequiresKeysRotateCap(t *testing.T) {
	f := &fakeQ{catalogZone: dbq.DnsCatalogZone{ID: uuid.New(), FabricID: uuid.New(), Signed: true}}
	rec := postJSON(t, mount(f), "/dns/catalog-zones/"+f.catalogZone.ID.String()+"/disable-dnssec", "", "dns:zones:update")
	if rec.Code != http.StatusForbidden {
		t.Errorf("got %d, want 403 without dns:keys:rotate", rec.Code)
	}
}
