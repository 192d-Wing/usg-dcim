// Tests for phase 4 — approval engine + reject + allocation reads +
// the carver. The fake querier records the ApproveLirRequest params
// so each scenario can assert what the CTE *would* have done; the
// carver itself runs for real against in-memory pool supernets.
package lir

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// ---- carver unit tests ----

func TestCarver_FirstFitLowest(t *testing.T) {
	parent := netip.MustParsePrefix("10.0.0.0/16")
	got, ok := findFirstFreePrefix(parent, 24, nil)
	if !ok || got != "10.0.0.0/24" {
		t.Errorf("expected 10.0.0.0/24, got %s ok=%v", got, ok)
	}
}

func TestCarver_SkipsOverlap(t *testing.T) {
	parent := netip.MustParsePrefix("10.0.0.0/16")
	occupied := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/24"),
		netip.MustParsePrefix("10.0.1.0/24"),
	}
	got, ok := findFirstFreePrefix(parent, 24, occupied)
	if !ok || got != "10.0.2.0/24" {
		t.Errorf("expected 10.0.2.0/24, got %s ok=%v", got, ok)
	}
}

func TestCarver_ExhaustionReturnsFalse(t *testing.T) {
	parent := netip.MustParsePrefix("10.0.0.0/24")
	occupied := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")}
	_, ok := findFirstFreePrefix(parent, 25, occupied)
	if ok {
		t.Error("fully-occupied parent should not yield a free prefix")
	}
}

func TestCarver_SizeSmallerThanParentIsInvalid(t *testing.T) {
	parent := netip.MustParsePrefix("10.0.0.0/24")
	_, ok := findFirstFreePrefix(parent, 16, nil)
	if ok {
		t.Error("size shallower than parent should fail")
	}
}

func TestCarver_HandlesV6(t *testing.T) {
	parent := netip.MustParsePrefix("2001:db8::/32")
	got, ok := findFirstFreePrefix(parent, 48, nil)
	if !ok || !strings.HasPrefix(got, "2001:db8") || !strings.HasSuffix(got, "/48") {
		t.Errorf("v6 carve expected 2001:db8*/48, got %s ok=%v", got, ok)
	}
}

// ---- fake querier additions for phase 4 ----

func (f *fakeQ) GetLandingFabric(_ context.Context, _ string) (dbq.LandingFabricRow, error) {
	if f.landing != nil {
		return *f.landing, nil
	}
	return dbq.LandingFabricRow{}, pgx.ErrNoRows
}

func (f *fakeQ) ListPoolSupernetsForCarve(_ context.Context, poolID uuid.UUID) ([]dbq.PoolSupernetForCarveRow, error) {
	return f.carveSources[poolID], nil
}

func (f *fakeQ) ListAllocatedPrefixesInPool(_ context.Context, poolID uuid.UUID) ([]dbq.AllocatedPrefixRow, error) {
	return f.carveAllocated[poolID], nil
}

func (f *fakeQ) ApproveLirRequest(_ context.Context, a dbq.ApproveLirRequestParams) (dbq.ApprovalResultRow, error) {
	f.approveCalled = &a
	// Honor the "stale-pending" simulation: when the test set the
	// request to a non-pending status, the CTE's RETURNING comes
	// back empty.
	if r, ok := f.requests[a.RequestID]; ok && r.Status != "pending_approval" {
		return dbq.ApprovalResultRow{}, pgx.ErrNoRows
	}
	allocID := uuid.New()
	tenantSupernetID := uuid.New()
	if f.requests == nil {
		f.requests = map[uuid.UUID]dbq.LirRequest{}
	}
	if r, ok := f.requests[a.RequestID]; ok {
		r.Status = "approved"
		r.ApprovedPoolID = &a.ApprovedPoolID
		r.DecisionNotes = a.DecisionNotes
		decidedBy := a.DecidedByUserID
		r.DecidedByUserID = &decidedBy
		f.requests[a.RequestID] = r
	}
	if f.allocations == nil {
		f.allocations = map[uuid.UUID]dbq.LirAllocation{}
	}
	alloc := dbq.LirAllocation{
		ID: allocID, RequestID: a.RequestID,
		OrganizationID: a.OrganizationID, PoolID: a.ApprovedPoolID,
		PoolSupernetID: a.PoolSupernetID, TenantSupernetID: tenantSupernetID,
		Prefix: a.Prefix, AllocatedByUserID: a.DecidedByUserID,
		Status: "active", ArinStatus: a.ArinInitialStatus,
	}
	f.allocations[allocID] = alloc
	// Return the post-update rows the CTE would have produced; the
	// handler reads result.Request / result.Allocation directly
	// rather than re-fetching.
	return dbq.ApprovalResultRow{
		Request:    f.requests[a.RequestID],
		Allocation: alloc,
	}, nil
}

func (f *fakeQ) RejectLirRequest(_ context.Context, a dbq.RejectLirRequestParams) (dbq.LirRequest, error) {
	r, ok := f.requests[a.ID]
	if !ok || r.Status != "pending_approval" {
		return dbq.LirRequest{}, pgx.ErrNoRows
	}
	r.Status = "rejected"
	notes := a.Reason
	r.DecisionNotes = &notes
	decidedBy := a.DecidedByUserID
	r.DecidedByUserID = &decidedBy
	f.requests[a.ID] = r
	return r, nil
}

func (f *fakeQ) GetLirAllocation(_ context.Context, id uuid.UUID) (dbq.LirAllocation, error) {
	if a, ok := f.allocations[id]; ok {
		return a, nil
	}
	return dbq.LirAllocation{}, pgx.ErrNoRows
}

func (f *fakeQ) ListLirAllocations(_ context.Context, p dbq.ListLirAllocationsParams) ([]dbq.LirAllocation, error) {
	out := []dbq.LirAllocation{}
	for _, a := range f.allocations {
		if !inOrgFilter(a.OrganizationID, p.ScopeOrgIds) {
			continue
		}
		if p.StatusFilter != nil && a.Status != *p.StatusFilter {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

func (f *fakeQ) CountLirAllocations(_ context.Context, p dbq.CountLirAllocationsParams) (int64, error) {
	var n int64
	for _, a := range f.allocations {
		if !inOrgFilter(a.OrganizationID, p.ScopeOrgIds) {
			continue
		}
		if p.StatusFilter != nil && a.Status != *p.StatusFilter {
			continue
		}
		n++
	}
	return n, nil
}

// ---- approve harness helpers ----

func setupApproveScenario(t *testing.T) (*fakeQ, uuid.UUID, uuid.UUID) {
	t.Helper()
	f := newFake()
	// Pool: v4, /20..29, enabled.
	poolID := uuid.New()
	f.pools[poolID] = dbq.LirPool{
		ID: poolID, IpFamily: 4, Enabled: true,
		MinPrefixLength: 20, MaxPrefixLength: 29,
	}
	// One source supernet: 10.0.0.0/16. Plenty of room.
	srcID := uuid.New()
	f.carveSources = map[uuid.UUID][]dbq.PoolSupernetForCarveRow{
		poolID: {{ID: srcID, Prefix: "10.0.0.0/16"}},
	}
	f.carveAllocated = map[uuid.UUID][]dbq.AllocatedPrefixRow{}
	// Landing fabric available.
	f.landing = &dbq.LandingFabricRow{
		FabricID: uuid.New(), DefaultVrfID: uuid.New(),
	}
	// Pending request asking for /24.
	reqID := uuid.New()
	orgID := uuid.New()
	f.requests = map[uuid.UUID]dbq.LirRequest{
		reqID: {
			ID: reqID, OrganizationID: orgID,
			RequesterUserID: uuid.New(), PoolID: &poolID,
			IpFamily: 4, PrefixLength: 24,
			Justification: "lab", Status: "pending_approval",
		},
	}
	return f, reqID, poolID
}

// ---- approve ----

func TestApprove_OK(t *testing.T) {
	f, reqID, poolID := setupApproveScenario(t)
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/requests/"+reqID.String()+"/approve", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if f.approveCalled == nil {
		t.Fatal("CTE call not recorded")
	}
	if f.approveCalled.Prefix != "10.0.0.0/24" {
		t.Errorf("first-fit should pick 10.0.0.0/24, got %s", f.approveCalled.Prefix)
	}
	if f.approveCalled.ApprovedPoolID != poolID {
		t.Errorf("approved_pool_id mismatch: %s vs %s", f.approveCalled.ApprovedPoolID, poolID)
	}
	if f.approveCalled.ArinInitialStatus != "none" {
		t.Errorf("no ARIN handle on pool → arin_initial should be 'none', got %s",
			f.approveCalled.ArinInitialStatus)
	}
	var body approveResponse
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.Request.Status != "approved" {
		t.Errorf("response request status: %s", body.Request.Status)
	}
}

func TestApprove_ArinInitialPendingWhenPoolHasHandle(t *testing.T) {
	f, reqID, poolID := setupApproveScenario(t)
	handle := "NET-198-51-100-0-1"
	p := f.pools[poolID]
	p.ArinParentNetHandle = &handle
	f.pools[poolID] = p
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/requests/"+reqID.String()+"/approve", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if f.approveCalled.ArinInitialStatus != "pending" {
		t.Errorf("expected arin_initial=pending, got %s", f.approveCalled.ArinInitialStatus)
	}
}

func TestApprove_RejectsNonPending(t *testing.T) {
	f, reqID, _ := setupApproveScenario(t)
	req := f.requests[reqID]
	req.Status = "approved"
	f.requests[reqID] = req
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/requests/"+reqID.String()+"/approve", nil)
	if rec.Code != http.StatusConflict {
		t.Errorf("got %d", rec.Code)
	}
}

func TestApprove_RejectsFamilyMismatch(t *testing.T) {
	f, reqID, poolID := setupApproveScenario(t)
	// Flip pool to v6 — request is v4.
	p := f.pools[poolID]
	p.IpFamily = 6
	f.pools[poolID] = p
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/requests/"+reqID.String()+"/approve", nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("got %d", rec.Code)
	}
}

func TestApprove_RejectsPrefixOutsidePoolBounds(t *testing.T) {
	f, reqID, poolID := setupApproveScenario(t)
	// Tighten the pool bounds so the request's /24 is out of range.
	p := f.pools[poolID]
	p.MinPrefixLength = 26
	p.MaxPrefixLength = 29
	f.pools[poolID] = p
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/requests/"+reqID.String()+"/approve", nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("got %d", rec.Code)
	}
}

func TestApprove_RejectsDisabledPool(t *testing.T) {
	f, reqID, poolID := setupApproveScenario(t)
	p := f.pools[poolID]
	p.Enabled = false
	f.pools[poolID] = p
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/requests/"+reqID.String()+"/approve", nil)
	if rec.Code != http.StatusConflict {
		t.Errorf("got %d", rec.Code)
	}
}

func TestApprove_PoolWithNoSourceSupernets(t *testing.T) {
	f, reqID, poolID := setupApproveScenario(t)
	f.carveSources[poolID] = nil
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/requests/"+reqID.String()+"/approve", nil)
	if rec.Code != http.StatusConflict {
		t.Errorf("got %d", rec.Code)
	}
}

func TestApprove_ExhaustedPool(t *testing.T) {
	f, reqID, poolID := setupApproveScenario(t)
	// Fill the /16 with a single /16 allocation so no /24 fits.
	src := f.carveSources[poolID][0]
	f.carveAllocated[poolID] = []dbq.AllocatedPrefixRow{
		{PoolSupernetID: src.ID, Prefix: "10.0.0.0/16"},
	}
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/requests/"+reqID.String()+"/approve", nil)
	if rec.Code != http.StatusConflict {
		t.Errorf("got %d", rec.Code)
	}
}

func TestApprove_CarverSkipsExistingAllocations(t *testing.T) {
	f, reqID, poolID := setupApproveScenario(t)
	src := f.carveSources[poolID][0]
	// First /24 already used → carver should pick the next.
	f.carveAllocated[poolID] = []dbq.AllocatedPrefixRow{
		{PoolSupernetID: src.ID, Prefix: "10.0.0.0/24"},
	}
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/requests/"+reqID.String()+"/approve", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if f.approveCalled.Prefix != "10.0.1.0/24" {
		t.Errorf("expected 10.0.1.0/24, got %s", f.approveCalled.Prefix)
	}
}

func TestApprove_NoLandingFabric_Returns500(t *testing.T) {
	f, reqID, _ := setupApproveScenario(t)
	f.landing = nil
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/requests/"+reqID.String()+"/approve", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("got %d", rec.Code)
	}
}

func TestApprove_OutOfScopeIs404(t *testing.T) {
	f, reqID, _ := setupApproveScenario(t)
	// Request's org is uuid X; principal is scoped to a different org.
	rec := do(t, mountWith(f, orgScopedPrincipal(uuid.New())),
		"POST", "/lir/requests/"+reqID.String()+"/approve", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}

func TestApprove_PoolOverride(t *testing.T) {
	f, reqID, _ := setupApproveScenario(t)
	overridePoolID := uuid.New()
	f.pools[overridePoolID] = dbq.LirPool{
		ID: overridePoolID, IpFamily: 4, Enabled: true,
		MinPrefixLength: 20, MaxPrefixLength: 29,
	}
	overrideSrcID := uuid.New()
	f.carveSources[overridePoolID] = []dbq.PoolSupernetForCarveRow{
		{ID: overrideSrcID, Prefix: "172.16.0.0/16"},
	}
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/requests/"+reqID.String()+"/approve", map[string]any{
			"approved_pool_id": overridePoolID.String(),
		})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if f.approveCalled.ApprovedPoolID != overridePoolID {
		t.Errorf("override not honored: %s", f.approveCalled.ApprovedPoolID)
	}
	if f.approveCalled.Prefix != "172.16.0.0/24" {
		t.Errorf("expected override-pool carve, got %s", f.approveCalled.Prefix)
	}
}

func TestApprove_NoPoolPreferenceNorOverride_Is422(t *testing.T) {
	f, reqID, _ := setupApproveScenario(t)
	req := f.requests[reqID]
	req.PoolID = nil
	f.requests[reqID] = req
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/requests/"+reqID.String()+"/approve", nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("got %d", rec.Code)
	}
}

// ---- reject ----

func TestReject_OK(t *testing.T) {
	f, reqID, _ := setupApproveScenario(t)
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/requests/"+reqID.String()+"/reject",
		map[string]any{"reason": "no business need"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if f.requests[reqID].Status != "rejected" {
		t.Errorf("status not rejected: %s", f.requests[reqID].Status)
	}
}

func TestReject_RequiresReason(t *testing.T) {
	f, reqID, _ := setupApproveScenario(t)
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/requests/"+reqID.String()+"/reject", map[string]any{"reason": ""})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("got %d", rec.Code)
	}
}

func TestReject_NonPendingIs409(t *testing.T) {
	f, reqID, _ := setupApproveScenario(t)
	r := f.requests[reqID]
	r.Status = "approved"
	f.requests[reqID] = r
	rec := do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/requests/"+reqID.String()+"/reject",
		map[string]any{"reason": "stale"})
	if rec.Code != http.StatusConflict {
		t.Errorf("got %d", rec.Code)
	}
}

func TestReject_OutOfScopeIs404(t *testing.T) {
	f, reqID, _ := setupApproveScenario(t)
	rec := do(t, mountWith(f, orgScopedPrincipal(uuid.New())),
		"POST", "/lir/requests/"+reqID.String()+"/reject",
		map[string]any{"reason": "x"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}

// ---- allocation reads ----

func TestListAllocations_GlobalSeesAll(t *testing.T) {
	f, reqID, _ := setupApproveScenario(t)
	// Approve to populate the allocation.
	do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/requests/"+reqID.String()+"/approve", nil)
	rec := do(t, mountWith(f, globalPrincipal()),
		"GET", "/lir/allocations/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	var body listAllocationsResponse
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.Total != 1 {
		t.Errorf("expected 1 allocation, got %d", body.Total)
	}
}

func TestListAllocations_OrgScopeFiltersDown(t *testing.T) {
	f, reqID, _ := setupApproveScenario(t)
	orgID := f.requests[reqID].OrganizationID
	do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/requests/"+reqID.String()+"/approve", nil)
	// Principal scoped to a different org sees nothing.
	rec := do(t, mountWith(f, orgScopedPrincipal(uuid.New())),
		"GET", "/lir/allocations/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	var body listAllocationsResponse
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.Total != 0 {
		t.Errorf("scope filter failed; expected 0, got %d", body.Total)
	}
	// Scoped to the right org → 1.
	rec = do(t, mountWith(f, orgScopedPrincipal(orgID)),
		"GET", "/lir/allocations/", nil)
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.Total != 1 {
		t.Errorf("expected 1 in-scope, got %d", body.Total)
	}
}

func TestGetAllocation_OutOfScopeIs404(t *testing.T) {
	f, reqID, _ := setupApproveScenario(t)
	do(t, mountWith(f, globalPrincipal()),
		"POST", "/lir/requests/"+reqID.String()+"/approve", nil)
	var allocID uuid.UUID
	for id := range f.allocations {
		allocID = id
	}
	rec := do(t, mountWith(f, orgScopedPrincipal(uuid.New())),
		"GET", "/lir/allocations/"+allocID.String(), nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}

func TestGetAllocation_NotFound(t *testing.T) {
	rec := do(t, mountWith(newFake(), globalPrincipal()),
		"GET", "/lir/allocations/"+uuid.New().String(), nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}
