// Tests for the LIR request lifecycle handlers (phase 3) — submit,
// list, get, cancel. Org-scope filter is exercised both ways: a
// global-scope principal sees everything, an org-scoped principal
// sees only requests in their organization set, an out-of-scope
// principal sees a 403 on submit and a 404 on get/cancel.
package lir

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

// ---- request methods on the fake querier ----

func (f *fakeQ) CreateLirRequest(_ context.Context, a dbq.CreateLirRequestParams) (dbq.LirRequest, error) {
	f.createdRequest = &a
	r := dbq.LirRequest{
		ID: uuid.New(), OrganizationID: a.OrganizationID,
		RequesterUserID: a.RequesterUserID, PoolID: a.PoolID, SiteID: a.SiteID,
		IpFamily: a.IpFamily, PrefixLength: a.PrefixLength,
		Purpose: a.Purpose, Classification: a.Classification,
		Justification: a.Justification, Status: "pending_approval",
	}
	if f.requests == nil {
		f.requests = map[uuid.UUID]dbq.LirRequest{}
	}
	f.requests[r.ID] = r
	return r, nil
}

func (f *fakeQ) GetLirRequest(_ context.Context, id uuid.UUID) (dbq.LirRequest, error) {
	if r, ok := f.requests[id]; ok {
		return r, nil
	}
	return dbq.LirRequest{}, pgx.ErrNoRows
}

func (f *fakeQ) ListLirRequests(_ context.Context, a dbq.ListLirRequestsParams) ([]dbq.LirRequest, error) {
	f.lastListReq = &a
	out := []dbq.LirRequest{}
	for _, r := range f.requests {
		if !inOrgFilter(r.OrganizationID, a.ScopeOrgIds) {
			continue
		}
		if a.StatusFilter != nil && r.Status != *a.StatusFilter {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeQ) CountLirRequests(_ context.Context, a dbq.CountLirRequestsParams) (int64, error) {
	var n int64
	for _, r := range f.requests {
		if !inOrgFilter(r.OrganizationID, a.ScopeOrgIds) {
			continue
		}
		if a.StatusFilter != nil && r.Status != *a.StatusFilter {
			continue
		}
		n++
	}
	return n, nil
}

func (f *fakeQ) CancelLirRequest(_ context.Context, a dbq.CancelLirRequestParams) (dbq.LirRequest, error) {
	r, ok := f.requests[a.ID]
	if !ok || r.Status != "pending_approval" {
		return dbq.LirRequest{}, pgx.ErrNoRows
	}
	r.Status = "cancelled"
	r.DecisionNotes = a.Notes
	f.requests[a.ID] = r
	return r, nil
}

// inOrgFilter mirrors the SQL: nil scope = global (match all); non-nil
// = restrict to listed orgs.
func inOrgFilter(orgID uuid.UUID, scope []uuid.UUID) bool {
	if scope == nil {
		return true
	}
	for _, o := range scope {
		if o == orgID {
			return true
		}
	}
	return false
}

// ---- harness with a configurable principal ----

func mountWith(f *fakeQ, p auth.Principal) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	})
	(&Handler{Q: f}).Mount(r)
	return r
}

func globalPrincipal() auth.Principal {
	return auth.Principal{
		Subject:      uuid.New(),
		Capabilities: []string{"*"},
		Label:        "test",
	}
}

func orgScopedPrincipal(orgIDs ...uuid.UUID) auth.Principal {
	set := make(map[uuid.UUID]struct{}, len(orgIDs))
	for _, id := range orgIDs {
		set[id] = struct{}{}
	}
	scope := auth.Scope{OrganizationIDs: set}
	return auth.Principal{
		Subject:      uuid.New(),
		Capabilities: []string{"*"},
		Scopes:       map[string]auth.Scope{"*": scope},
		Label:        "test-scoped",
	}
}

// ---- submit ----

func TestSubmit_Valid(t *testing.T) {
	f := newFake()
	orgID := uuid.New()
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/requests/", map[string]any{
			"organization_id": orgID.String(),
			"ip_family":       4, "prefix_length": 28,
			"justification": "need a /28 for new lab segment",
		})
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if f.createdRequest == nil || f.createdRequest.OrganizationID != orgID {
		t.Errorf("create not recorded: %+v", f.createdRequest)
	}
	if f.createdRequest.RequesterUserID == uuid.Nil {
		t.Errorf("requester not propagated from principal")
	}
}

func TestSubmit_RejectsEmptyJustification(t *testing.T) {
	rec := do(t, mountWith(newFake(), globalPrincipal()),
		"POST", "/lir/requests/", map[string]any{
			"organization_id": uuid.New().String(),
			"ip_family":       4, "prefix_length": 28,
			"justification": "",
		})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("got %d", rec.Code)
	}
}

func TestSubmit_RejectsMissingOrgID(t *testing.T) {
	rec := do(t, mountWith(newFake(), globalPrincipal()),
		"POST", "/lir/requests/", map[string]any{
			"ip_family": 4, "prefix_length": 28, "justification": "x",
		})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d", rec.Code)
	}
}

func TestSubmit_RejectsV4PrefixOver32(t *testing.T) {
	rec := do(t, mountWith(newFake(), globalPrincipal()),
		"POST", "/lir/requests/", map[string]any{
			"organization_id": uuid.New().String(),
			"ip_family":       4, "prefix_length": 40,
			"justification": "x",
		})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("got %d", rec.Code)
	}
}

func TestSubmit_OrgScopeAllowsInScopeOrg(t *testing.T) {
	f := newFake()
	allowed := uuid.New()
	p := orgScopedPrincipal(allowed)
	rec := do(t, mountWith(f, p), "POST", "/lir/requests/", map[string]any{
		"organization_id": allowed.String(),
		"ip_family":       4, "prefix_length": 28,
		"justification": "x",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSubmit_OrgScopeRejectsOutOfScope(t *testing.T) {
	allowed := uuid.New()
	other := uuid.New()
	p := orgScopedPrincipal(allowed)
	rec := do(t, mountWith(newFake(), p), "POST", "/lir/requests/", map[string]any{
		"organization_id": other.String(),
		"ip_family":       4, "prefix_length": 28,
		"justification": "x",
	})
	if rec.Code != http.StatusForbidden {
		t.Errorf("got %d", rec.Code)
	}
}

// ---- list ----

func TestList_GlobalSeesAll(t *testing.T) {
	f := newFake()
	f.requests = map[uuid.UUID]dbq.LirRequest{}
	for i := 0; i < 3; i++ {
		id := uuid.New()
		f.requests[id] = dbq.LirRequest{ID: id, OrganizationID: uuid.New(), Status: "pending_approval"}
	}
	rec := do(t, mountWith(f, globalPrincipal()), "GET", "/lir/requests/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	var body listRequestsResponse
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.Total != 3 {
		t.Errorf("global should see all 3, got total=%d", body.Total)
	}
	// Global → scope_org_ids must be nil so SQL skips the filter.
	if f.lastListReq == nil || f.lastListReq.ScopeOrgIds != nil {
		t.Errorf("global should pass nil ScopeOrgIds, got %+v", f.lastListReq)
	}
}

func TestList_OrgScopedSeesOnlyMatching(t *testing.T) {
	f := newFake()
	orgA, orgB := uuid.New(), uuid.New()
	f.requests = map[uuid.UUID]dbq.LirRequest{}
	for _, org := range []uuid.UUID{orgA, orgA, orgB} {
		id := uuid.New()
		f.requests[id] = dbq.LirRequest{ID: id, OrganizationID: org, Status: "pending_approval"}
	}
	rec := do(t, mountWith(f, orgScopedPrincipal(orgA)), "GET", "/lir/requests/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	var body listRequestsResponse
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.Total != 2 {
		t.Errorf("orgA-scoped should see 2, got total=%d", body.Total)
	}
	if f.lastListReq == nil || len(f.lastListReq.ScopeOrgIds) != 1 || f.lastListReq.ScopeOrgIds[0] != orgA {
		t.Errorf("expected ScopeOrgIds=[orgA], got %+v", f.lastListReq)
	}
}

func TestList_ScopedWithEmptyOrgSetShortCircuits(t *testing.T) {
	f := newFake()
	id := uuid.New()
	f.requests = map[uuid.UUID]dbq.LirRequest{
		id: {ID: id, OrganizationID: uuid.New(), Status: "pending_approval"},
	}
	// orgScopedPrincipal with no UUIDs → non-global with empty OrganizationIDs.
	p := orgScopedPrincipal()
	rec := do(t, mountWith(f, p), "GET", "/lir/requests/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	var body listRequestsResponse
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.Total != 0 || len(body.Items) != 0 {
		t.Errorf("scoped-empty should return empty page, got %+v", body)
	}
	// No SQL should run on the empty-scope path.
	if f.lastListReq != nil {
		t.Errorf("expected no list query when scope is empty")
	}
}

func TestList_StatusFilter(t *testing.T) {
	f := newFake()
	f.requests = map[uuid.UUID]dbq.LirRequest{}
	addReq := func(s string) {
		id := uuid.New()
		f.requests[id] = dbq.LirRequest{ID: id, OrganizationID: uuid.New(), Status: s}
	}
	addReq("pending_approval")
	addReq("pending_approval")
	addReq("approved")
	rec := do(t, mountWith(f, globalPrincipal()),
		"GET", "/lir/requests/?status=pending_approval", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	var body listRequestsResponse
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.Total != 2 {
		t.Errorf("status=pending_approval should match 2, got %d", body.Total)
	}
}

// ---- get ----

func TestGet_NotFound(t *testing.T) {
	rec := do(t, mountWith(newFake(), globalPrincipal()),
		"GET", "/lir/requests/"+uuid.New().String()+"/", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}

func TestGet_OutOfScopeIs404(t *testing.T) {
	f := newFake()
	id := uuid.New()
	otherOrg := uuid.New()
	f.requests = map[uuid.UUID]dbq.LirRequest{
		id: {ID: id, OrganizationID: otherOrg, Status: "pending_approval"},
	}
	allowed := uuid.New()
	rec := do(t, mountWith(f, orgScopedPrincipal(allowed)),
		"GET", "/lir/requests/"+id.String()+"/", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("out-of-scope get should 404 not leak, got %d", rec.Code)
	}
}

// ---- cancel ----

func TestCancel_OK(t *testing.T) {
	f := newFake()
	id := uuid.New()
	orgID := uuid.New()
	f.requests = map[uuid.UUID]dbq.LirRequest{
		id: {ID: id, OrganizationID: orgID, Status: "pending_approval"},
	}
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/requests/"+id.String()+"/cancel", map[string]any{
			"notes": "duplicate of earlier request",
		})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if f.requests[id].Status != "cancelled" {
		t.Errorf("status not flipped to cancelled: %+v", f.requests[id])
	}
}

func TestCancel_EmptyBodyIsAllowed(t *testing.T) {
	f := newFake()
	id := uuid.New()
	f.requests = map[uuid.UUID]dbq.LirRequest{
		id: {ID: id, OrganizationID: uuid.New(), Status: "pending_approval"},
	}
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/requests/"+id.String()+"/cancel", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("empty body should still cancel, got %d", rec.Code)
	}
}

func TestCancel_NotPendingIs409(t *testing.T) {
	f := newFake()
	id := uuid.New()
	f.requests = map[uuid.UUID]dbq.LirRequest{
		id: {ID: id, OrganizationID: uuid.New(), Status: "approved"},
	}
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/requests/"+id.String()+"/cancel", nil)
	if rec.Code != http.StatusConflict {
		t.Errorf("got %d", rec.Code)
	}
}

func TestCancel_OutOfScopeIs404(t *testing.T) {
	f := newFake()
	id := uuid.New()
	otherOrg := uuid.New()
	f.requests = map[uuid.UUID]dbq.LirRequest{
		id: {ID: id, OrganizationID: otherOrg, Status: "pending_approval"},
	}
	rec := do(t, mountWith(f, orgScopedPrincipal(uuid.New())),
		"POST", "/lir/requests/"+id.String()+"/cancel", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}
