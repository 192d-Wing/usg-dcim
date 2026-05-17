package organization

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
	last dbq.ListOrganizationsParams
}

func (f *fakeQ) ListOrganizations(_ context.Context, a dbq.ListOrganizationsParams) ([]dbq.Organization, error) {
	f.last = a
	return nil, nil
}
func (f *fakeQ) CountOrganizations(_ context.Context) (int64, error) { return 0, nil }
func (f *fakeQ) GetOrganization(_ context.Context, _ uuid.UUID) (dbq.Organization, error) {
	return dbq.Organization{}, pgx.ErrNoRows
}
func (f *fakeQ) CreateOrganization(_ context.Context, a dbq.CreateOrganizationParams) (dbq.Organization, error) {
	return dbq.Organization{ID: uuid.New(), Name: a.Name, Country: a.Country, City: a.City, AddressLine1: a.AddressLine1}, nil
}
func (f *fakeQ) UpdateOrganization(_ context.Context, a dbq.UpdateOrganizationParams) (dbq.Organization, error) {
	return dbq.Organization{ID: a.ID}, nil
}
func (f *fakeQ) CountAsnsForOrganization(_ context.Context, _ uuid.UUID) (int64, error) {
	return 0, nil
}
func (f *fakeQ) DeleteOrganization(_ context.Context, _ uuid.UUID) error { return nil }

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

func TestList_Defaults(t *testing.T) {
	f := &fakeQ{}
	rec := do(t, mount(f), "/organizations/")
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if f.last.Limit != 50 || f.last.Offset != 0 {
		t.Errorf("defaults: limit=%d offset=%d", f.last.Limit, f.last.Offset)
	}
}

func TestList_PageSizeAlias(t *testing.T) {
	f := &fakeQ{}
	do(t, mount(f), "/organizations/?page_size=5&offset=10")
	if f.last.Limit != 5 || f.last.Offset != 10 {
		t.Errorf("page_size: got limit=%d offset=%d", f.last.Limit, f.last.Offset)
	}
}

func TestGet_NotFound(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/organizations/"+uuid.New().String())
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}

func TestGet_BadUUID(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/organizations/x")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d", rec.Code)
	}
}
