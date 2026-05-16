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
	lastAsn   dbq.ListAsnsParams
	lastPL    dbq.ListPrefixListsParams
	lastEntry dbq.ListPrefixListEntriesParams
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
	do(t, mount(f), "/asns?kind=private")
	if f.lastAsn.Kind == nil || *f.lastAsn.Kind != "private" {
		t.Errorf("kind not threaded: %+v", f.lastAsn)
	}
}

func TestListPrefixLists_FamilyFilter(t *testing.T) {
	f := &fakeQ{}
	do(t, mount(f), "/prefix-lists?family=ipv6")
	if f.lastPL.Family == nil || *f.lastPL.Family != "ipv6" {
		t.Errorf("family not threaded: %+v", f.lastPL)
	}
}

func TestListPrefixListEntries_PrefixListIDFilter(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{}
	do(t, mount(f), "/prefix-list-entries?prefix_list_id="+id.String())
	if f.lastEntry.PrefixListID == nil || *f.lastEntry.PrefixListID != id {
		t.Errorf("prefix_list_id not threaded: %+v", f.lastEntry)
	}
}

func TestListPrefixListEntries_BadPrefixListID(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/prefix-list-entries?prefix_list_id=x")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d", rec.Code)
	}
}
