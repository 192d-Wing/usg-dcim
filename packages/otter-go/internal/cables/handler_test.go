package cables

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

type fakeQ struct{ last dbq.ListCablesParams }

func (f *fakeQ) ListCables(_ context.Context, a dbq.ListCablesParams) ([]dbq.Cable, error) {
	f.last = a
	return nil, nil
}
func (f *fakeQ) CountCables(_ context.Context, _ dbq.CountCablesParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) GetCable(_ context.Context, _ uuid.UUID) (dbq.Cable, error) {
	return dbq.Cable{}, pgx.ErrNoRows
}
func (f *fakeQ) CreateCable(_ context.Context, a dbq.CreateCableParams) (dbq.Cable, error) {
	return dbq.Cable{ID: uuid.New(), SiteID: a.SiteID, AAssetID: a.AAssetID, BAssetID: a.BAssetID}, nil
}
func (f *fakeQ) DeleteCable(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) GetAssetSiteID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.New(), nil
}
func (f *fakeQ) GetAsset(_ context.Context, id uuid.UUID) (dbq.Asset, error) {
	return dbq.Asset{ID: id, SiteID: uuid.New(), Name: "stub"}, nil
}
func (f *fakeQ) FindCableForPort(_ context.Context, _ dbq.FindCableForPortParams) (dbq.FindCableForPortRow, error) {
	return dbq.FindCableForPortRow{}, pgx.ErrNoRows
}
func (f *fakeQ) UpdateCable(_ context.Context, a dbq.UpdateCableParams) (dbq.Cable, error) {
	return dbq.Cable{ID: a.ID}, nil
}
func (f *fakeQ) GetSiteRegionID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, pgx.ErrNoRows
}
func (f *fakeQ) GetSiteOrganizationID(_ context.Context, _ uuid.UUID) (*uuid.UUID, error) {
	return nil, nil
}
func (f *fakeQ) ListSiteGroupIDsForSite(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}
func (f *fakeQ) ListSiteIDsForExpansion(_ context.Context, _ dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error) {
	return nil, nil
}

func mount(f *fakeQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}
func do(t *testing.T, h http.Handler, p string) *httptest.ResponseRecorder {
	t.Helper()
	// Wildcard principal — inventory:cables:read gate added when cables
	// moved fully onto otter-go blocks otherwise-anonymous test traffic.
	req := authtest.Request("GET", p, authtest.PrincipalWithCaps("*"), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestListCables_FilterThreading(t *testing.T) {
	sid, aid, rid := uuid.New(), uuid.New(), uuid.New()
	f := &fakeQ{}
	rec := do(t, mount(f), "/cables?site_id="+sid.String()+"&asset_id="+aid.String()+"&rack_id="+rid.String())
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if f.last.SiteID == nil || *f.last.SiteID != sid {
		t.Error("site_id")
	}
	if f.last.AssetID == nil || *f.last.AssetID != aid {
		t.Error("asset_id")
	}
	if f.last.RackID == nil || *f.last.RackID != rid {
		t.Error("rack_id")
	}
}

func TestListCables_BadUUID(t *testing.T) {
	for _, p := range []string{"/cables?site_id=x", "/cables?asset_id=x", "/cables?rack_id=x"} {
		rec := do(t, mount(&fakeQ{}), p)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d", p, rec.Code)
		}
	}
}

func TestGetCable_NotFound(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/cables/"+uuid.New().String())
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}
