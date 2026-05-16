package collectors

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
	last     dbq.ListCollectorsParams
	getCalls int
}

func (f *fakeQ) ListCollectors(_ context.Context, a dbq.ListCollectorsParams) ([]dbq.Collector, error) {
	f.last = a
	return nil, nil
}
func (f *fakeQ) CountCollectors(_ context.Context, _ dbq.CountCollectorsParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) GetCollector(_ context.Context, _ uuid.UUID) (dbq.Collector, error) {
	f.getCalls++
	return dbq.Collector{}, pgx.ErrNoRows
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

func TestList_FiltersThreaded(t *testing.T) {
	sid := uuid.New()
	f := &fakeQ{}
	rec := do(t, mount(f), "/collectors/?site_id="+sid.String()+"&status=healthy&page_size=10")
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if f.last.SiteID == nil || *f.last.SiteID != sid {
		t.Error("site_id not threaded")
	}
	if f.last.Status == nil || *f.last.Status != "healthy" {
		t.Error("status not threaded")
	}
	if f.last.Limit != 10 {
		t.Errorf("page_size alias: want 10, got %d", f.last.Limit)
	}
}

func TestList_BadSiteUUID(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/collectors/?site_id=x")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d", rec.Code)
	}
}

func TestGet_NotFound(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/collectors/"+uuid.New().String())
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}

func TestGet_BadUUID(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/collectors/not-a-uuid")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d", rec.Code)
	}
}
