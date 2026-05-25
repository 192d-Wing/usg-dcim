package locations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

type fakeQ struct {
	buildings []dbq.Building
	rooms     []dbq.Room
	rows      []dbq.Row
	lastB     dbq.ListBuildingsParams
	lastR     dbq.ListRoomsParams
	lastRow   dbq.ListRowsParams
}

func (f *fakeQ) ListBuildings(_ context.Context, a dbq.ListBuildingsParams) ([]dbq.Building, error) {
	f.lastB = a
	return f.buildings, nil
}
func (f *fakeQ) CountBuildings(_ context.Context, _ dbq.CountBuildingsParams) (int64, error) {
	return int64(len(f.buildings)), nil
}
func (f *fakeQ) ListRooms(_ context.Context, a dbq.ListRoomsParams) ([]dbq.Room, error) {
	f.lastR = a
	return f.rooms, nil
}
func (f *fakeQ) CountRooms(_ context.Context, _ dbq.CountRoomsParams) (int64, error) {
	return int64(len(f.rooms)), nil
}
func (f *fakeQ) ListRows(_ context.Context, a dbq.ListRowsParams) ([]dbq.Row, error) {
	f.lastRow = a
	return f.rows, nil
}
func (f *fakeQ) CountRows(_ context.Context, _ dbq.CountRowsParams) (int64, error) {
	return int64(len(f.rows)), nil
}
func (f *fakeQ) CreateBuilding(_ context.Context, a dbq.CreateBuildingParams) (dbq.Building, error) {
	return dbq.Building{ID: uuid.New(), SiteID: a.SiteID, Name: a.Name, Code: a.Code}, nil
}
func (f *fakeQ) CreateRoom(_ context.Context, a dbq.CreateRoomParams) (dbq.Room, error) {
	return dbq.Room{ID: uuid.New(), BuildingID: a.BuildingID, Name: a.Name, Code: a.Code, FloorAreaSqft: a.FloorAreaSqft}, nil
}
func (f *fakeQ) CreateRow(_ context.Context, a dbq.CreateRowParams) (dbq.Row, error) {
	return dbq.Row{ID: uuid.New(), RoomID: a.RoomID, Name: a.Name, Code: a.Code}, nil
}

// PR 63 — site-scope expansion for buildings LIST scope filter.
func (f *fakeQ) ListSiteIDsForExpansion(_ context.Context, _ dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error) {
	return nil, nil
}

func mount(f *fakeQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}

func do(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

func TestListBuildings_OK_WithFilter(t *testing.T) {
	sid := uuid.New()
	f := &fakeQ{buildings: []dbq.Building{{ID: uuid.New(), SiteID: sid, Code: "B1"}}}
	rec := do(t, mount(f), "/buildings?site_id="+sid.String())
	if rec.Code != 200 {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if f.lastB.SiteID == nil || *f.lastB.SiteID != sid {
		t.Errorf("site_id not threaded: %+v", f.lastB)
	}
	var body buildingsPage
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Total != 1 || body.Items[0].Code != "B1" {
		t.Errorf("body wrong: %+v", body)
	}
}

func TestListBuildings_BadSiteID(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/buildings?site_id=not-a-uuid")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestListRooms_BadBuildingID(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/rooms?building_id=x")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestListRows_OK_WithFilter(t *testing.T) {
	rid := uuid.New()
	f := &fakeQ{rows: []dbq.Row{{ID: uuid.New(), RoomID: rid, Code: "R1"}}}
	rec := do(t, mount(f), "/rows?room_id="+rid.String())
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if f.lastRow.RoomID == nil || *f.lastRow.RoomID != rid {
		t.Errorf("room_id not threaded: %+v", f.lastRow)
	}
}

func TestListBuildings_DefaultLimit(t *testing.T) {
	f := &fakeQ{}
	do(t, mount(f), "/buildings")
	if f.lastB.Limit != 50 || f.lastB.Offset != 0 {
		t.Errorf("default page params wrong: %+v", f.lastB)
	}
}

// ----- PR 96: rooms / rows pick up the ABAC SiteIds expansion -----

func TestListRooms_PassesSiteIdsForGlobalPrincipal(t *testing.T) {
	// Global principal (no scopes) → ScopedSiteFilter returns nil,
	// scoped=false. The handler passes SiteIds=nil to the LIST,
	// which the SQL treats as "no filter."
	f := &fakeQ{}
	do(t, mount(f), "/rooms")
	if f.lastR.SiteIds != nil {
		t.Errorf("global principal should see nil SiteIds, got %v", f.lastR.SiteIds)
	}
}

func TestListRows_PassesSiteIdsForGlobalPrincipal(t *testing.T) {
	f := &fakeQ{}
	do(t, mount(f), "/rows")
	if f.lastRow.SiteIds != nil {
		t.Errorf("global principal should see nil SiteIds, got %v", f.lastRow.SiteIds)
	}
}

func TestListRoomsParams_HasSiteIds(t *testing.T) {
	// Wiring contract — the generated param struct must carry the
	// SiteIds slice the handler threads through. If sqlc regen
	// drops it, this catches the drift.
	var p dbq.ListRoomsParams
	p.SiteIds = []uuid.UUID{uuid.New()}
	if len(p.SiteIds) != 1 {
		t.Error("ListRoomsParams.SiteIds not present")
	}
}

func TestListRowsParams_HasSiteIds(t *testing.T) {
	var p dbq.ListRowsParams
	p.SiteIds = []uuid.UUID{uuid.New()}
	if len(p.SiteIds) != 1 {
		t.Error("ListRowsParams.SiteIds not present")
	}
}
