package racks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

type fakeQ struct {
	list func(ctx context.Context, a dbq.ListRacksParams) ([]dbq.Rack, error)
	get  func(ctx context.Context, id uuid.UUID) (dbq.Rack, error)
}

func (f *fakeQ) ListRacks(ctx context.Context, a dbq.ListRacksParams) ([]dbq.Rack, error) {
	if f.list != nil {
		return f.list(ctx, a)
	}
	return nil, nil
}
func (f *fakeQ) CountRacks(_ context.Context, _ dbq.CountRacksParams) (int64, error) { return 0, nil }
func (f *fakeQ) GetRack(ctx context.Context, id uuid.UUID) (dbq.Rack, error) {
	if f.get != nil {
		return f.get(ctx, id)
	}
	return dbq.Rack{}, pgx.ErrNoRows
}
func (f *fakeQ) CreateRack(_ context.Context, a dbq.CreateRackParams) (dbq.Rack, error) {
	return dbq.Rack{ID: uuid.New(), SiteID: a.SiteID, RowID: a.RowID, Name: a.Name, Code: a.Code, UHeight: a.UHeight}, nil
}
func (f *fakeQ) UpdateRack(_ context.Context, a dbq.UpdateRackParams) (dbq.Rack, error) {
	return dbq.Rack{ID: a.ID}, nil
}
func (f *fakeQ) GetRackAssetsForShrinkCheck(_ context.Context, _ uuid.UUID) ([]dbq.RackPlacedAsset, error) {
	return nil, nil
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

// PR 62 — scope expansion. Default returns nil (no expansion); the
// scoped tests override via scopedFakeQ.
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
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
	return rec
}

func TestListRacks_FilterThreading(t *testing.T) {
	sid, rid := uuid.New(), uuid.New()
	var got dbq.ListRacksParams
	f := &fakeQ{list: func(_ context.Context, a dbq.ListRacksParams) ([]dbq.Rack, error) {
		got = a
		return nil, nil
	}}
	rec := do(t, mount(f), "/racks?site_id="+sid.String()+"&row_id="+rid.String()+"&limit=10")
	if rec.Code != 200 {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if got.SiteID == nil || *got.SiteID != sid {
		t.Errorf("site_id missing")
	}
	if got.RowID == nil || *got.RowID != rid {
		t.Errorf("row_id missing")
	}
	if got.Limit != 10 {
		t.Errorf("limit: %d", got.Limit)
	}
}

func TestListRacks_BadFilters(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/racks?site_id=x")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("site_id: got %d", rec.Code)
	}
	rec = do(t, mount(&fakeQ{}), "/racks?row_id=x")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("row_id: got %d", rec.Code)
	}
}

func TestListRacks_DBError(t *testing.T) {
	f := &fakeQ{list: func(context.Context, dbq.ListRacksParams) ([]dbq.Rack, error) {
		return nil, errors.New("boom")
	}}
	rec := do(t, mount(f), "/racks")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("got %d", rec.Code)
	}
}

func TestGetRack_OK(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{get: func(_ context.Context, gid uuid.UUID) (dbq.Rack, error) {
		return dbq.Rack{ID: id, Code: "R42", UHeight: 42}, nil
	}}
	rec := do(t, mount(f), "/racks/"+id.String())
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	var r dbq.Rack
	_ = json.Unmarshal(rec.Body.Bytes(), &r)
	if r.UHeight != 42 || r.Code != "R42" {
		t.Errorf("body wrong: %+v", r)
	}
}

func TestGetRack_NotFound(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/racks/"+uuid.New().String())
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}

func TestGetRack_BadID(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/racks/not-uuid")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d", rec.Code)
	}
}
