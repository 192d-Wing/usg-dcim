package bgp

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
	lastAsn   dbq.ListAsnsParams
	lastPL    dbq.ListPrefixListsParams
	lastEntry dbq.ListPrefixListEntriesParams
	lastCL    dbq.ListCommunityListsParams
	lastCLE   dbq.ListCommunityListEntriesParams
	lastRM    dbq.ListRouteMapsParams
	lastRME   dbq.ListRouteMapEntriesParams
}

func (f *fakeQ) ListAsns(_ context.Context, a dbq.ListAsnsParams) ([]dbq.Asn, error) {
	f.lastAsn = a
	return nil, nil
}
func (f *fakeQ) CountAsns(_ context.Context, _ dbq.CountAsnsParams) (int64, error) { return 0, nil }
func (f *fakeQ) ListPrefixLists(_ context.Context, a dbq.ListPrefixListsParams) ([]dbq.PrefixList, error) {
	f.lastPL = a
	return nil, nil
}
func (f *fakeQ) CountPrefixLists(_ context.Context, _ dbq.CountPrefixListsParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) ListPrefixListEntries(_ context.Context, a dbq.ListPrefixListEntriesParams) ([]dbq.PrefixListEntry, error) {
	f.lastEntry = a
	return nil, nil
}
func (f *fakeQ) CountPrefixListEntries(_ context.Context, _ dbq.CountPrefixListEntriesParams) (int64, error) {
	return 0, nil
}

func (f *fakeQ) ListCommunityLists(_ context.Context, a dbq.ListCommunityListsParams) ([]dbq.CommunityList, error) {
	f.lastCL = a
	return nil, nil
}
func (f *fakeQ) CountCommunityLists(_ context.Context, _ dbq.CountCommunityListsParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) ListCommunityListEntries(_ context.Context, a dbq.ListCommunityListEntriesParams) ([]dbq.CommunityListEntry, error) {
	f.lastCLE = a
	return nil, nil
}
func (f *fakeQ) CountCommunityListEntries(_ context.Context, _ dbq.CountCommunityListEntriesParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) ListRouteMaps(_ context.Context, a dbq.ListRouteMapsParams) ([]dbq.RouteMap, error) {
	f.lastRM = a
	return nil, nil
}
func (f *fakeQ) CountRouteMaps(_ context.Context) (int64, error) { return 0, nil }
func (f *fakeQ) ListRouteMapEntries(_ context.Context, a dbq.ListRouteMapEntriesParams) ([]dbq.RouteMapEntry, error) {
	f.lastRME = a
	return nil, nil
}
func (f *fakeQ) CountRouteMapEntries(_ context.Context, _ dbq.CountRouteMapEntriesParams) (int64, error) {
	return 0, nil
}

// ---- Mutation stubs (PR 44) ----

func (f *fakeQ) CreateAsn(_ context.Context, a dbq.CreateAsnParams) (dbq.Asn, error) {
	return dbq.Asn{ID: uuid.New(), Asn: a.Asn, Name: a.Name, Kind: a.Kind}, nil
}
func (f *fakeQ) UpdateAsn(_ context.Context, a dbq.UpdateAsnParams) (dbq.Asn, error) {
	return dbq.Asn{ID: a.ID}, nil
}
func (f *fakeQ) DeleteAsn(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreatePrefixList(_ context.Context, a dbq.CreatePrefixListParams) (dbq.PrefixList, error) {
	return dbq.PrefixList{ID: uuid.New(), Name: a.Name, Family: a.Family}, nil
}
func (f *fakeQ) UpdatePrefixList(_ context.Context, a dbq.UpdatePrefixListParams) (dbq.PrefixList, error) {
	return dbq.PrefixList{ID: a.ID}, nil
}
func (f *fakeQ) DeletePrefixList(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreatePrefixListEntry(_ context.Context, a dbq.CreatePrefixListEntryParams) (dbq.PrefixListEntry, error) {
	return dbq.PrefixListEntry{ID: uuid.New(), PrefixListID: a.PrefixListID, Seq: a.Seq, Action: a.Action, Prefix: a.Prefix}, nil
}
func (f *fakeQ) UpdatePrefixListEntry(_ context.Context, a dbq.UpdatePrefixListEntryParams) (dbq.PrefixListEntry, error) {
	return dbq.PrefixListEntry{ID: a.ID}, nil
}
func (f *fakeQ) DeletePrefixListEntry(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreateCommunityList(_ context.Context, a dbq.CreateCommunityListParams) (dbq.CommunityList, error) {
	return dbq.CommunityList{ID: uuid.New(), Name: a.Name, Kind: a.Kind}, nil
}
func (f *fakeQ) UpdateCommunityList(_ context.Context, a dbq.UpdateCommunityListParams) (dbq.CommunityList, error) {
	return dbq.CommunityList{ID: a.ID}, nil
}
func (f *fakeQ) DeleteCommunityList(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreateCommunityListEntry(_ context.Context, a dbq.CreateCommunityListEntryParams) (dbq.CommunityListEntry, error) {
	return dbq.CommunityListEntry{ID: uuid.New(), CommunityListID: a.CommunityListID, Seq: a.Seq, Action: a.Action, Value: a.Value}, nil
}
func (f *fakeQ) UpdateCommunityListEntry(_ context.Context, a dbq.UpdateCommunityListEntryParams) (dbq.CommunityListEntry, error) {
	return dbq.CommunityListEntry{ID: a.ID}, nil
}
func (f *fakeQ) DeleteCommunityListEntry(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreateRouteMap(_ context.Context, a dbq.CreateRouteMapParams) (dbq.RouteMap, error) {
	return dbq.RouteMap{ID: uuid.New(), Name: a.Name}, nil
}
func (f *fakeQ) UpdateRouteMap(_ context.Context, a dbq.UpdateRouteMapParams) (dbq.RouteMap, error) {
	return dbq.RouteMap{ID: a.ID}, nil
}
func (f *fakeQ) DeleteRouteMap(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreateRouteMapEntry(_ context.Context, a dbq.CreateRouteMapEntryParams) (dbq.RouteMapEntry, error) {
	return dbq.RouteMapEntry{ID: uuid.New(), RouteMapID: a.RouteMapID, Seq: a.Seq, Action: a.Action}, nil
}
func (f *fakeQ) UpdateRouteMapEntry(_ context.Context, a dbq.UpdateRouteMapEntryParams) (dbq.RouteMapEntry, error) {
	return dbq.RouteMapEntry{ID: a.ID}, nil
}
func (f *fakeQ) DeleteRouteMapEntry(_ context.Context, _ uuid.UUID) error { return nil }

// ---- TCP AO key-chains fakes ----
//
// These are overridden case-by-case in tcp_ao_test.go via the closures
// on `tcpAoFake` (test-local). The package-shared fakeQ just satisfies
// the interface with sane defaults.
func (f *fakeQ) ListTcpAoKeyChains(_ context.Context, _ dbq.ListTcpAoKeyChainsParams) ([]dbq.TcpAoKeyChain, error) {
	return nil, nil
}
func (f *fakeQ) CountTcpAoKeyChains(_ context.Context) (int64, error) { return 0, nil }
func (f *fakeQ) GetTcpAoKeyChain(_ context.Context, _ uuid.UUID) (dbq.TcpAoKeyChain, error) {
	return dbq.TcpAoKeyChain{}, pgx.ErrNoRows
}
func (f *fakeQ) CreateTcpAoKeyChain(_ context.Context, a dbq.CreateTcpAoKeyChainParams) (dbq.TcpAoKeyChain, error) {
	return dbq.TcpAoKeyChain{ID: uuid.New(), Name: a.Name, Description: a.Description}, nil
}
func (f *fakeQ) UpdateTcpAoKeyChain(_ context.Context, a dbq.UpdateTcpAoKeyChainParams) (dbq.TcpAoKeyChain, error) {
	return dbq.TcpAoKeyChain{ID: a.ID}, nil
}
func (f *fakeQ) DeleteTcpAoKeyChain(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CountKeysInTcpAoKeyChain(_ context.Context, _ uuid.UUID) (int64, error) {
	return 0, nil
}

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

func TestListAsns_KindFilter(t *testing.T) {
	f := &fakeQ{}
	do(t, mount(f), "/bgp/asns?kind=private")
	if f.lastAsn.Kind == nil || *f.lastAsn.Kind != "private" {
		t.Errorf("kind not threaded: %+v", f.lastAsn)
	}
}

func TestListPrefixLists_FamilyFilter(t *testing.T) {
	f := &fakeQ{}
	do(t, mount(f), "/bgp/prefix-lists?family=ipv6")
	if f.lastPL.Family == nil || *f.lastPL.Family != "ipv6" {
		t.Errorf("family not threaded: %+v", f.lastPL)
	}
}

func TestListPrefixListEntries_PrefixListIDFilter(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{}
	do(t, mount(f), "/bgp/prefix-list-entries?prefix_list_id="+id.String())
	if f.lastEntry.PrefixListID == nil || *f.lastEntry.PrefixListID != id {
		t.Errorf("prefix_list_id not threaded: %+v", f.lastEntry)
	}
}

func TestListPrefixListEntries_BadPrefixListID(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/bgp/prefix-list-entries?prefix_list_id=x")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d", rec.Code)
	}
}

func TestListAsns_PageSizeAlias(t *testing.T) {
	f := &fakeQ{}
	do(t, mount(f), "/bgp/asns?page_size=200")
	if f.lastAsn.Limit != 200 {
		t.Errorf("page_size not honored: %d", f.lastAsn.Limit)
	}
}

func TestListCommunityLists_KindFilter(t *testing.T) {
	f := &fakeQ{}
	do(t, mount(f), "/bgp/community-lists?kind=standard")
	if f.lastCL.Kind == nil || *f.lastCL.Kind != "standard" {
		t.Errorf("kind not threaded: %+v", f.lastCL)
	}
}

func TestListCommunityListEntries_Filter(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{}
	do(t, mount(f), "/bgp/community-list-entries?community_list_id="+id.String())
	if f.lastCLE.CommunityListID == nil || *f.lastCLE.CommunityListID != id {
		t.Errorf("community_list_id not threaded: %+v", f.lastCLE)
	}
}

func TestListCommunityListEntries_BadID(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/bgp/community-list-entries?community_list_id=x")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d", rec.Code)
	}
}

func TestListRouteMaps_PageSize(t *testing.T) {
	f := &fakeQ{}
	do(t, mount(f), "/bgp/route-maps?page_size=42")
	if f.lastRM.Limit != 42 {
		t.Errorf("page_size not honored: %d", f.lastRM.Limit)
	}
}

func TestListRouteMapEntries_Filter(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{}
	do(t, mount(f), "/bgp/route-map-entries?route_map_id="+id.String())
	if f.lastRME.RouteMapID == nil || *f.lastRME.RouteMapID != id {
		t.Errorf("route_map_id not threaded: %+v", f.lastRME)
	}
}

func TestListRouteMapEntries_BadID(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/bgp/route-map-entries?route_map_id=x")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d", rec.Code)
	}
}
