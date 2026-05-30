package locations

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

func doMethod(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf []byte
	if body != nil {
		buf, _ = json.Marshal(body)
	}
	req := authtest.Request(method, path, authtest.PrincipalWithCaps("*"), bytes.NewReader(buf))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestUpdateBuilding_OK(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	f := &fakeQ{getBuilding: dbq.Building{ID: id, SiteID: sid, Name: "old", Code: "B1"}}
	newName := "renamed"
	rec := doMethod(t, mount(f), http.MethodPatch, "/buildings/"+id.String(), map[string]any{"name": newName})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if f.lastUpdateB.Name == nil || *f.lastUpdateB.Name != newName {
		t.Errorf("name not threaded: %+v", f.lastUpdateB)
	}
	if f.lastUpdateB.Code != nil {
		t.Errorf("code should be nil when omitted, got %v", *f.lastUpdateB.Code)
	}
}

func TestUpdateBuilding_NotFound(t *testing.T) {
	f := &fakeQ{getBuildingErr: pgx.ErrNoRows}
	rec := doMethod(t, mount(f), http.MethodPatch, "/buildings/"+uuid.New().String(), map[string]any{"name": "x"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestUpdateBuilding_BadID(t *testing.T) {
	rec := doMethod(t, mount(&fakeQ{}), http.MethodPatch, "/buildings/not-a-uuid", map[string]any{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestDeleteBuilding_OK(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	f := &fakeQ{getBuilding: dbq.Building{ID: id, SiteID: sid}}
	rec := doMethod(t, mount(f), http.MethodDelete, "/buildings/"+id.String(), nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(f.deleted) != 1 || f.deleted[0] != "building:"+id.String() {
		t.Errorf("delete not called: %v", f.deleted)
	}
}

func TestDeleteBuilding_NotFound(t *testing.T) {
	f := &fakeQ{getBuildingErr: pgx.ErrNoRows}
	rec := doMethod(t, mount(f), http.MethodDelete, "/buildings/"+uuid.New().String(), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestDeleteBuilding_FKViolation_Returns409(t *testing.T) {
	f := &fakeQ{
		getBuilding: dbq.Building{ID: uuid.New(), SiteID: uuid.New()},
		// Mirror what pgx hands back when a row's children still exist.
		deleteErr: httpx.ErrFKViolation,
	}
	rec := doMethod(t, mount(f), http.MethodDelete, "/buildings/"+uuid.New().String(), nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 (has dependent rows); got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestUpdateRoom_OK_ExplicitNullClearsFloorArea(t *testing.T) {
	id := uuid.New()
	existingSqft := int32(120)
	f := &fakeQ{
		getRoom:       dbq.Room{ID: id, BuildingID: uuid.New(), Name: "old", Code: "R1", FloorAreaSqft: &existingSqft},
		siteIDForRoom: uuid.New(),
	}
	rec := doMethod(t, mount(f), http.MethodPatch, "/rooms/"+id.String(), map[string]any{"floor_area_sqft": nil})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if !f.lastUpdateR.FloorAreaSqftSet {
		t.Fatal("FloorAreaSqftSet should be true for explicit null")
	}
	if f.lastUpdateR.FloorAreaSqft != nil {
		t.Errorf("FloorAreaSqft should be nil for explicit null, got %v", *f.lastUpdateR.FloorAreaSqft)
	}
}

func TestUpdateRoom_OmittedFloorAreaKeepsCurrent(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{
		getRoom:       dbq.Room{ID: id, BuildingID: uuid.New(), Name: "old"},
		siteIDForRoom: uuid.New(),
	}
	rec := doMethod(t, mount(f), http.MethodPatch, "/rooms/"+id.String(), map[string]any{"name": "x"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if f.lastUpdateR.FloorAreaSqftSet {
		t.Error("FloorAreaSqftSet must be false when the key was omitted from the body")
	}
}

func TestDeleteRoom_OK(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{
		getRoom:       dbq.Room{ID: id, BuildingID: uuid.New()},
		siteIDForRoom: uuid.New(),
	}
	rec := doMethod(t, mount(f), http.MethodDelete, "/rooms/"+id.String(), nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(f.deleted) != 1 || f.deleted[0] != "room:"+id.String() {
		t.Errorf("delete not called: %v", f.deleted)
	}
}

func TestUpdateRow_OK(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{
		getRow:       dbq.Row{ID: id, RoomID: uuid.New(), Name: "old", Code: "ROW1"},
		siteIDForRow: uuid.New(),
	}
	newName := "renamed"
	rec := doMethod(t, mount(f), http.MethodPatch, "/rows/"+id.String(), map[string]any{"name": newName})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if f.lastUpdateRow.Name == nil || *f.lastUpdateRow.Name != newName {
		t.Errorf("name not threaded: %+v", f.lastUpdateRow)
	}
}

func TestDeleteRow_OK(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{
		getRow:       dbq.Row{ID: id, RoomID: uuid.New()},
		siteIDForRow: uuid.New(),
	}
	rec := doMethod(t, mount(f), http.MethodDelete, "/rows/"+id.String(), nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
}

// Scope denials: principal holds the cap but is scoped to a different
// site. EnforceSiteScope returns ErrOutsideScope → 403.
func TestPATCHDelete_OutOfScope_403(t *testing.T) {
	id, sid, otherSite := uuid.New(), uuid.New(), uuid.New()
	scope := auth.Scope{SiteIDs: map[uuid.UUID]struct{}{otherSite: {}}}

	cases := []struct {
		name   string
		method string
		path   string
		cap    string
		setup  func() *fakeQ
	}{
		{
			"updateBuilding", http.MethodPatch, "/buildings/" + id.String(), "inventory:buildings:update",
			func() *fakeQ { return &fakeQ{getBuilding: dbq.Building{ID: id, SiteID: sid}} },
		},
		{
			"deleteBuilding", http.MethodDelete, "/buildings/" + id.String(), "inventory:buildings:delete",
			func() *fakeQ { return &fakeQ{getBuilding: dbq.Building{ID: id, SiteID: sid}} },
		},
		{
			"updateRoom", http.MethodPatch, "/rooms/" + id.String(), "inventory:rooms:update",
			func() *fakeQ { return &fakeQ{getRoom: dbq.Room{ID: id}, siteIDForRoom: sid} },
		},
		{
			"deleteRoom", http.MethodDelete, "/rooms/" + id.String(), "inventory:rooms:delete",
			func() *fakeQ { return &fakeQ{getRoom: dbq.Room{ID: id}, siteIDForRoom: sid} },
		},
		{
			"updateRow", http.MethodPatch, "/rows/" + id.String(), "inventory:rows:update",
			func() *fakeQ { return &fakeQ{getRow: dbq.Row{ID: id}, siteIDForRow: sid} },
		},
		{
			"deleteRow", http.MethodDelete, "/rows/" + id.String(), "inventory:rows:delete",
			func() *fakeQ { return &fakeQ{getRow: dbq.Row{ID: id}, siteIDForRow: sid} },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := authtest.PrincipalWithScopes([]string{tc.cap}, map[string]auth.Scope{tc.cap: scope})
			buf, _ := json.Marshal(map[string]any{"name": "x"})
			req := authtest.Request(tc.method, tc.path, p, bytes.NewReader(buf))
			rec := httptest.NewRecorder()
			mount(tc.setup()).ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected 403, got %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestUpdateRoom_FloorAreaWrongType_400(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{
		getRoom:       dbq.Room{ID: id, BuildingID: uuid.New()},
		siteIDForRoom: uuid.New(),
	}
	// floor_area_sqft must be an integer; a string is a client error.
	body := `{"floor_area_sqft": "not-an-int"}`
	req := authtest.Request(http.MethodPatch, "/rooms/"+id.String(), authtest.PrincipalWithCaps("*"), bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	mount(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}
