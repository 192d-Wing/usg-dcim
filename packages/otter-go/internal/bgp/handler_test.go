package bgp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

type fakeQ struct {
	lastAsn    dbq.ListAsnsParams
	lastPL     dbq.ListPrefixListsParams
	lastEntry  dbq.ListPrefixListEntriesParams
	lastCL     dbq.ListCommunityListsParams
	lastCLE    dbq.ListCommunityListEntriesParams
	lastRM     dbq.ListRouteMapsParams
	lastRME    dbq.ListRouteMapEntriesParams
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
