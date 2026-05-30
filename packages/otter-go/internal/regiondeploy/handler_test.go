package regiondeploy

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

type fakeQ struct {
	list           []dbq.RegionDeploymentSummary
	lastListParams dbq.ListRegionDeploymentsParams
	getRow         dbq.RegionDeployment
	getErr         error
	nodes          []dbq.RegionDeploymentNode
	services       []dbq.RegionDeploymentService
	events         []dbq.RegionDeploymentEvent
	lastEventArg   dbq.ListRegionDeploymentEventsParams
	siteRegionErr  error
}

func (f *fakeQ) ListRegionDeployments(_ context.Context, a dbq.ListRegionDeploymentsParams) ([]dbq.RegionDeploymentSummary, error) {
	f.lastListParams = a
	return f.list, nil
}
func (f *fakeQ) CountRegionDeployments(_ context.Context, _ []uuid.UUID) (int64, error) {
	return int64(len(f.list)), nil
}
func (f *fakeQ) GetRegionDeployment(_ context.Context, _ uuid.UUID) (dbq.RegionDeployment, error) {
	return f.getRow, f.getErr
}
func (f *fakeQ) ListRegionDeploymentNodes(_ context.Context, _ uuid.UUID) ([]dbq.RegionDeploymentNode, error) {
	return f.nodes, nil
}
func (f *fakeQ) ListRegionDeploymentServices(_ context.Context, _ uuid.UUID) ([]dbq.RegionDeploymentService, error) {
	return f.services, nil
}
func (f *fakeQ) ListRegionDeploymentEvents(_ context.Context, a dbq.ListRegionDeploymentEventsParams) ([]dbq.RegionDeploymentEvent, error) {
	f.lastEventArg = a
	return f.events, nil
}
func (f *fakeQ) ListSiteIDsForExpansion(_ context.Context, _ dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error) {
	return nil, nil
}
func (f *fakeQ) GetSiteRegionID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, f.siteRegionErr
}
func (f *fakeQ) GetSiteOrganizationID(_ context.Context, _ uuid.UUID) (*uuid.UUID, error) {
	return nil, nil
}
func (f *fakeQ) ListSiteGroupIDsForSite(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

// audit.Recorder satisfaction: nothing exercises it on reads, but the
// Handler struct requires it. Stub returns nil.
func (f *fakeQ) Record(_ context.Context, _ ...any) error { return nil }

func mount(f *fakeQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f, Audit: nil}).Mount(r)
	return r
}

func doReq(t *testing.T, h http.Handler, p auth.Principal, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := authtest.Request(http.MethodGet, path, p, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func wildcardP() auth.Principal { return authtest.PrincipalWithCaps("*") }

func TestList_OK_GlobalPrincipal(t *testing.T) {
	sid := uuid.New()
	f := &fakeQ{list: []dbq.RegionDeploymentSummary{
		{ID: uuid.New(), SiteID: sid, Name: "edge-7", Status: "ready"},
	}}
	rec := doReq(t, mount(f), wildcardP(), "/region-deployments")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if f.lastListParams.ScopeSiteIds != nil {
		t.Errorf("global principal should get nil scope, got %v", f.lastListParams.ScopeSiteIds)
	}
}

func TestList_EmptyScope_NoListCall(t *testing.T) {
	called := false
	f := &fakeQ{}
	// Wrap ListRegionDeployments so we can detect the call without
	// embedding fakeQ. Simplest: capture via a sentinel principal.
	scope := auth.Scope{Enclaves: map[string]struct{}{"u": {}}}
	p := authtest.PrincipalWithScopes(
		[]string{capRead}, map[string]auth.Scope{capRead: scope},
	)
	h := &Handler{Q: &listGuardQ{fakeQ: f, called: &called}}
	r := chi.NewRouter()
	h.Mount(r)
	req := authtest.Request(http.MethodGet, "/region-deployments", p, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	var body listResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 0 || len(body.Items) != 0 {
		t.Errorf("expected empty page; got %+v", body)
	}
	if called {
		t.Error("ListRegionDeployments must not run when scope reaches zero sites")
	}
}

type listGuardQ struct {
	*fakeQ
	called *bool
}

func (g *listGuardQ) ListRegionDeployments(ctx context.Context, a dbq.ListRegionDeploymentsParams) ([]dbq.RegionDeploymentSummary, error) {
	*g.called = true
	return g.fakeQ.ListRegionDeployments(ctx, a)
}

func TestGet_OK_ShapesMatchPython(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	cfg := json.RawMessage(`{"edge_mode":true}`)
	f := &fakeQ{
		getRow: dbq.RegionDeployment{
			ID: id, SiteID: sid, Name: "edge-7", Status: "ready",
			Config: cfg,
		},
		nodes: []dbq.RegionDeploymentNode{
			{ID: uuid.New(), Hostname: "n01", Mac: "aa:bb:cc:dd:ee:ff", BmcAddress: "10.0.0.1", Role: "control_plane", Status: "ready"},
		},
		services: []dbq.RegionDeploymentService{
			{ID: uuid.New(), Service: "dns_auth", Status: "ready"},
		},
	}
	rec := doReq(t, mount(f), wildcardP(), "/region-deployments/"+id.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	var body detailOut
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Name != "edge-7" || body.Status != "ready" {
		t.Errorf("top-level fields wrong: %+v", body)
	}
	if string(body.Config) != `{"edge_mode":true}` {
		t.Errorf("config not threaded: %s", body.Config)
	}
	if len(body.Nodes) != 1 || body.Nodes[0].Hostname != "n01" {
		t.Errorf("nodes wrong: %+v", body.Nodes)
	}
	if len(body.Services) != 1 || body.Services[0].Service != "dns_auth" {
		t.Errorf("services wrong: %+v", body.Services)
	}
}

func TestGet_EmptyConfigSerializedAsObject(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	// pg returns nil for a row with default '{}'::jsonb only if the
	// scan hands back zero bytes; mirror that here.
	f := &fakeQ{getRow: dbq.RegionDeployment{
		ID: id, SiteID: sid, Name: "stub", Status: "pending", Config: nil,
	}}
	rec := doReq(t, mount(f), wildcardP(), "/region-deployments/"+id.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	// Body should serialize config as `{}` so the finch wizard's
	// JSON editor doesn't see `null`.
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"config":{}`)) {
		t.Errorf("config should serialize as {}; body=%s", rec.Body.String())
	}
}

func TestGet_NotFound(t *testing.T) {
	f := &fakeQ{getErr: pgx.ErrNoRows}
	rec := doReq(t, mount(f), wildcardP(), "/region-deployments/"+uuid.New().String())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestGet_BadID(t *testing.T) {
	rec := doReq(t, mount(&fakeQ{}), wildcardP(), "/region-deployments/not-a-uuid")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestGet_OutOfScope_403(t *testing.T) {
	id, sid, otherSite := uuid.New(), uuid.New(), uuid.New()
	f := &fakeQ{getRow: dbq.RegionDeployment{
		ID: id, SiteID: sid, Name: "secret", Status: "ready",
	}}
	scope := auth.Scope{SiteIDs: map[uuid.UUID]struct{}{otherSite: {}}}
	p := authtest.PrincipalWithScopes([]string{capRead}, map[string]auth.Scope{capRead: scope})
	rec := doReq(t, mount(f), p, "/region-deployments/"+id.String())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestListEvents_OK_DefaultParams(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	f := &fakeQ{
		getRow: dbq.RegionDeployment{ID: id, SiteID: sid, Status: "ready"},
		events: []dbq.RegionDeploymentEvent{
			{ID: 1, Stage: "preflight", Level: "info", Message: "started"},
		},
	}
	rec := doReq(t, mount(f), wildcardP(), "/region-deployments/"+id.String()+"/events")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if f.lastEventArg.Since != 0 || f.lastEventArg.Limit != 500 {
		t.Errorf("default cursor wrong: since=%d limit=%d", f.lastEventArg.Since, f.lastEventArg.Limit)
	}
}

func TestListEvents_RespectsSinceCursor(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	f := &fakeQ{getRow: dbq.RegionDeployment{ID: id, SiteID: sid, Status: "ready"}}
	rec := doReq(t, mount(f), wildcardP(), "/region-deployments/"+id.String()+"/events?since=42&limit=10")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if f.lastEventArg.Since != 42 || f.lastEventArg.Limit != 10 {
		t.Errorf("cursor not threaded: %+v", f.lastEventArg)
	}
}

func TestListEvents_BadSince_400(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	f := &fakeQ{getRow: dbq.RegionDeployment{ID: id, SiteID: sid, Status: "ready"}}
	rec := doReq(t, mount(f), wildcardP(), "/region-deployments/"+id.String()+"/events?since=-1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestListEvents_LimitOutOfRange_400(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	f := &fakeQ{getRow: dbq.RegionDeployment{ID: id, SiteID: sid, Status: "ready"}}
	rec := doReq(t, mount(f), wildcardP(), "/region-deployments/"+id.String()+"/events?limit=999999")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestListEvents_OutOfScope_403(t *testing.T) {
	id, sid, otherSite := uuid.New(), uuid.New(), uuid.New()
	f := &fakeQ{getRow: dbq.RegionDeployment{ID: id, SiteID: sid, Status: "ready"}}
	scope := auth.Scope{SiteIDs: map[uuid.UUID]struct{}{otherSite: {}}}
	p := authtest.PrincipalWithScopes([]string{capRead}, map[string]auth.Scope{capRead: scope})
	rec := doReq(t, mount(f), p, "/region-deployments/"+id.String()+"/events")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestListEvents_NotFound(t *testing.T) {
	f := &fakeQ{getErr: pgx.ErrNoRows}
	rec := doReq(t, mount(f), wildcardP(), "/region-deployments/"+uuid.New().String()+"/events")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d", rec.Code)
	}
}
