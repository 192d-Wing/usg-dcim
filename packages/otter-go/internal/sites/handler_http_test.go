package sites

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// fakeQuerier is an in-memory implementation of the Querier interface so
// the HTTP handlers can be exercised end-to-end without a real Postgres.
// Each method honors a per-test override so a single test can return an
// error from one query and good data from another.
type fakeQuerier struct {
	list       func(ctx context.Context, arg dbq.ListSitesParams) ([]dbq.Site, error)
	count      func(ctx context.Context, arg dbq.CountSitesParams) (int64, error)
	get        func(ctx context.Context, id uuid.UUID) (dbq.Site, error)
	expandSite func(ctx context.Context, arg dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error)
}

func (f *fakeQuerier) ListSites(ctx context.Context, arg dbq.ListSitesParams) ([]dbq.Site, error) {
	if f.list != nil {
		return f.list(ctx, arg)
	}
	return nil, nil
}

func (f *fakeQuerier) CountSites(ctx context.Context, arg dbq.CountSitesParams) (int64, error) {
	if f.count != nil {
		return f.count(ctx, arg)
	}
	return 0, nil
}

func (f *fakeQuerier) GetSite(ctx context.Context, id uuid.UUID) (dbq.Site, error) {
	if f.get != nil {
		return f.get(ctx, id)
	}
	return dbq.Site{}, pgx.ErrNoRows
}

func (f *fakeQuerier) CreateSite(_ context.Context, arg dbq.CreateSiteParams) (dbq.Site, error) {
	return dbq.Site{ID: uuid.New(), RegionID: arg.RegionID, Name: arg.Name, Code: arg.Code,
		LifecycleState: arg.LifecycleState}, nil
}

func (f *fakeQuerier) UpdateSite(_ context.Context, arg dbq.UpdateSiteParams) (dbq.Site, error) {
	return dbq.Site{ID: arg.ID}, nil
}

func (f *fakeQuerier) GetSiteRegionID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *fakeQuerier) GetSiteOrganizationID(_ context.Context, _ uuid.UUID) (*uuid.UUID, error) {
	return nil, nil
}
func (f *fakeQuerier) ListSiteGroupIDsForSite(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

// PR 62 — scope-filtered LIST helper. Tests that don't care about scope
// can let this return nil (treated as "no expansion" by the caller).
// expandSite is a per-test override so a single test can return a
// specific set of allowed site IDs.
func (f *fakeQuerier) ListSiteIDsForExpansion(ctx context.Context, arg dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error) {
	if f.expandSite != nil {
		return f.expandSite(ctx, arg)
	}
	return nil, nil
}

// mount returns a chi router wired with the handler against f. Mirrors
// what main.go does for /api/v1/* minus auth (the auth middleware is
// covered in internal/auth/middleware_test.go).
func mount(f *fakeQuerier) http.Handler {
	r := chi.NewRouter()
	h := &Handler{Q: f}
	h.Mount(r)
	return r
}

func do(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestListSites_OK(t *testing.T) {
	siteID := uuid.New()
	regionID := uuid.New()
	f := &fakeQuerier{
		list: func(_ context.Context, arg dbq.ListSitesParams) ([]dbq.Site, error) {
			if arg.Limit != 50 || arg.Offset != 0 {
				t.Errorf("default pagination wrong: limit=%d offset=%d", arg.Limit, arg.Offset)
			}
			return []dbq.Site{{ID: siteID, RegionID: regionID, Name: "Site A", Code: "AAA"}}, nil
		},
		count: func(context.Context, dbq.CountSitesParams) (int64, error) { return 1, nil },
	}
	rec := do(t, mount(f), "GET", "/sites")
	if rec.Code != 200 {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	var body listResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 1 || len(body.Items) != 1 || body.Items[0].Code != "AAA" {
		t.Errorf("unexpected body: %+v", body)
	}
	if body.Limit != 50 || body.Offset != 0 {
		t.Errorf("default page params not echoed: limit=%d offset=%d", body.Limit, body.Offset)
	}
}

func TestListSites_FiltersThreaded(t *testing.T) {
	regionID := uuid.New()
	var captured dbq.ListSitesParams
	f := &fakeQuerier{
		list: func(_ context.Context, arg dbq.ListSitesParams) ([]dbq.Site, error) {
			captured = arg
			return nil, nil
		},
	}
	url := "/sites?region_id=" + regionID.String() +
		"&majcom=AFMC&enclave=siprnet&organization=192d&lifecycle_state=active" +
		"&limit=25&offset=100"
	rec := do(t, mount(f), "GET", url)
	if rec.Code != 200 {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if captured.Limit != 25 || captured.Offset != 100 {
		t.Errorf("pagination not threaded: %+v", captured)
	}
	if captured.RegionID == nil || *captured.RegionID != regionID {
		t.Errorf("region_id not threaded: got %v", captured.RegionID)
	}
	if captured.Majcom == nil || *captured.Majcom != "AFMC" {
		t.Errorf("majcom not threaded")
	}
	if captured.LifecycleState == nil || *captured.LifecycleState != "active" {
		t.Errorf("lifecycle_state not threaded")
	}
}

func TestListSites_BadRegionID(t *testing.T) {
	rec := do(t, mount(&fakeQuerier{}), "GET", "/sites?region_id=not-a-uuid")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "region_id") {
		t.Errorf("error body should mention region_id: %s", rec.Body.String())
	}
}

func TestListSites_LimitClamped(t *testing.T) {
	var captured dbq.ListSitesParams
	f := &fakeQuerier{
		list: func(_ context.Context, arg dbq.ListSitesParams) ([]dbq.Site, error) {
			captured = arg
			return nil, nil
		},
	}
	do(t, mount(f), "GET", "/sites?limit=99999&offset=-5")
	if captured.Limit != 500 {
		t.Errorf("limit not clamped: %d", captured.Limit)
	}
	if captured.Offset != 0 {
		t.Errorf("offset not clamped: %d", captured.Offset)
	}
}

func TestListSites_DBError_Returns500(t *testing.T) {
	boom := errors.New("connection refused")
	f := &fakeQuerier{
		list: func(context.Context, dbq.ListSitesParams) ([]dbq.Site, error) { return nil, boom },
	}
	rec := do(t, mount(f), "GET", "/sites")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500", rec.Code)
	}
}

func TestGetSite_OK(t *testing.T) {
	id := uuid.New()
	f := &fakeQuerier{
		get: func(_ context.Context, gotID uuid.UUID) (dbq.Site, error) {
			if gotID != id {
				t.Errorf("wrong id: got %s want %s", gotID, id)
			}
			return dbq.Site{ID: id, Name: "Solo Site", Code: "SOLO"}, nil
		},
	}
	rec := do(t, mount(f), "GET", "/sites/"+id.String())
	if rec.Code != 200 {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	var got dbq.Site
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Code != "SOLO" {
		t.Errorf("body wrong: %+v", got)
	}
}

func TestGetSite_NotFound(t *testing.T) {
	rec := do(t, mount(&fakeQuerier{}), "GET", "/sites/"+uuid.New().String())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "site not found") {
		t.Errorf("body should say site not found: %s", rec.Body.String())
	}
}

func TestGetSite_BadID(t *testing.T) {
	rec := do(t, mount(&fakeQuerier{}), "GET", "/sites/not-a-uuid")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "uuid") {
		t.Errorf("body should mention uuid: %s", rec.Body.String())
	}
}

func TestGetSite_DBError_Returns500(t *testing.T) {
	f := &fakeQuerier{
		get: func(context.Context, uuid.UUID) (dbq.Site, error) {
			return dbq.Site{}, errors.New("connection refused")
		},
	}
	rec := do(t, mount(f), "GET", "/sites/"+uuid.New().String())
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500", rec.Code)
	}
}

func TestErrorBodyShape_MatchesFastAPI(t *testing.T) {
	// finch parses {"detail": "..."} for errors; if we ever change the
	// shape, this test surfaces it so the cutover doesn't break the UI.
	rec := do(t, mount(&fakeQuerier{}), "GET", "/sites/bad")
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body should be JSON: %s", rec.Body.String())
	}
	if _, ok := body["detail"]; !ok {
		t.Errorf("error body missing 'detail' key: %v", body)
	}
}
