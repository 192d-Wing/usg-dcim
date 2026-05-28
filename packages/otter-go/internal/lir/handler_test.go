// Tests for the LIR pool handlers. Mirrors the organization slice's
// pattern: fake Querier in-memory, a real chi router with the auth
// middleware bypassed (handlers run with a stub principal that holds
// `*`), and table-driven HTTP assertions.
package lir

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
)

// ---- fake querier ----

type fakeQ struct {
	pools       map[uuid.UUID]dbq.LirPool
	supernets   map[uuid.UUID]dbq.SupernetLirAttachRow
	requests    map[uuid.UUID]dbq.LirRequest
	allocations map[uuid.UUID]dbq.LirAllocation
	// carver inputs, keyed by pool_id
	carveSources   map[uuid.UUID][]dbq.PoolSupernetForCarveRow
	carveAllocated map[uuid.UUID][]dbq.AllocatedPrefixRow
	// system landing fabric — nil to simulate the deployment-missing
	// path (GetLandingFabric returns ErrNoRows).
	landing *dbq.LandingFabricRow
	// recorded effects
	created        *dbq.CreateLirPoolParams
	updated        *dbq.UpdateLirPoolParams
	deleted        *uuid.UUID
	attached       *dbq.AttachSupernetToPoolParams
	detached       *dbq.DetachSupernetFromPoolParams
	createdRequest *dbq.CreateLirRequestParams
	lastListReq    *dbq.ListLirRequestsParams
	approveCalled  *dbq.ApproveLirRequestParams
	// controls for failure-path tests
	allocCountByPool         map[uuid.UUID]int64
	allocCountByPoolSupernet map[uuid.UUID]int64
}

func newFake() *fakeQ {
	return &fakeQ{
		pools:                    map[uuid.UUID]dbq.LirPool{},
		supernets:                map[uuid.UUID]dbq.SupernetLirAttachRow{},
		allocCountByPool:         map[uuid.UUID]int64{},
		allocCountByPoolSupernet: map[uuid.UUID]int64{},
	}
}

func (f *fakeQ) ListLirPools(_ context.Context, _ dbq.ListLirPoolsParams) ([]dbq.LirPool, error) {
	out := make([]dbq.LirPool, 0, len(f.pools))
	for _, p := range f.pools {
		out = append(out, p)
	}
	return out, nil
}
func (f *fakeQ) CountLirPools(_ context.Context) (int64, error) {
	return int64(len(f.pools)), nil
}
func (f *fakeQ) GetLirPool(_ context.Context, id uuid.UUID) (dbq.LirPool, error) {
	if p, ok := f.pools[id]; ok {
		return p, nil
	}
	return dbq.LirPool{}, pgx.ErrNoRows
}
func (f *fakeQ) CreateLirPool(_ context.Context, a dbq.CreateLirPoolParams) (dbq.LirPool, error) {
	f.created = &a
	p := dbq.LirPool{
		ID: uuid.New(), Name: a.Name, Slug: a.Slug,
		IpFamily: a.IpFamily, MinPrefixLength: a.MinPrefixLength, MaxPrefixLength: a.MaxPrefixLength,
		Enabled: true,
	}
	f.pools[p.ID] = p
	return p, nil
}
func (f *fakeQ) UpdateLirPool(_ context.Context, a dbq.UpdateLirPoolParams) (dbq.LirPool, error) {
	f.updated = &a
	p, ok := f.pools[a.ID]
	if !ok {
		return dbq.LirPool{}, pgx.ErrNoRows
	}
	if a.Name != nil {
		p.Name = *a.Name
	}
	if a.MinPrefixLength != nil {
		p.MinPrefixLength = *a.MinPrefixLength
	}
	if a.MaxPrefixLength != nil {
		p.MaxPrefixLength = *a.MaxPrefixLength
	}
	if a.Enabled != nil {
		p.Enabled = *a.Enabled
	}
	f.pools[a.ID] = p
	return p, nil
}
func (f *fakeQ) DeleteLirPool(_ context.Context, id uuid.UUID) error {
	f.deleted = &id
	delete(f.pools, id)
	return nil
}
func (f *fakeQ) CountAllocationsForPool(_ context.Context, id uuid.UUID) (int64, error) {
	return f.allocCountByPool[id], nil
}
func (f *fakeQ) ListPoolSourceSupernets(_ context.Context, _ dbq.ListPoolSourceSupernetsParams) ([]dbq.PoolSourceSupernetRow, error) {
	return nil, nil
}
func (f *fakeQ) CountPoolSourceSupernets(_ context.Context, _ uuid.UUID) (int64, error) {
	return 0, nil
}
func (f *fakeQ) GetSupernetForLirAttach(_ context.Context, id uuid.UUID) (dbq.SupernetLirAttachRow, error) {
	if s, ok := f.supernets[id]; ok {
		return s, nil
	}
	return dbq.SupernetLirAttachRow{}, pgx.ErrNoRows
}
func (f *fakeQ) AttachSupernetToPool(_ context.Context, a dbq.AttachSupernetToPoolParams) error {
	f.attached = &a
	s := f.supernets[a.ID]
	s.LirPoolID = &a.PoolID
	f.supernets[a.ID] = s
	return nil
}
func (f *fakeQ) DetachSupernetFromPool(_ context.Context, a dbq.DetachSupernetFromPoolParams) error {
	f.detached = &a
	s := f.supernets[a.ID]
	s.LirPoolID = nil
	f.supernets[a.ID] = s
	return nil
}
func (f *fakeQ) DetachAllPoolSupernets(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CountAllocationsForPoolSupernet(_ context.Context, id uuid.UUID) (int64, error) {
	return f.allocCountByPoolSupernet[id], nil
}

// ResetArinJobForRetry mirrors the SQL's WHERE arin_status IN
// ('failed', 'none') guard so the test surface includes the
// already-pending / already-registered no-op behavior.
func (f *fakeQ) ResetArinJobForRetry(_ context.Context, id uuid.UUID) error {
	a, ok := f.allocations[id]
	if !ok {
		return nil
	}
	if a.ArinStatus != "failed" && a.ArinStatus != "none" {
		return nil
	}
	a.ArinStatus = "pending"
	a.ArinAttempts = 0
	a.ArinNetHandle = nil
	a.ArinLastAttemptAt = nil
	a.ArinLastError = nil
	f.allocations[id] = a
	return nil
}

// RequestReturnLirAllocation mirrors the SQL WHERE status='active'
// guard: only active allocations flip to return_requested; anything
// else makes RETURNING empty (pgx.ErrNoRows) so the handler returns
// 409.
func (f *fakeQ) RequestReturnLirAllocation(_ context.Context, arg dbq.RequestReturnLirAllocationParams) (dbq.LirAllocation, error) {
	a, ok := f.allocations[arg.ID]
	if !ok || a.Status != "active" {
		return dbq.LirAllocation{}, pgx.ErrNoRows
	}
	a.Status = "return_requested"
	by := arg.ReturnRequestedByUserID
	a.ReturnRequestedByUserID = &by
	a.ReturnReason = &arg.ReturnReason
	f.allocations[arg.ID] = a
	return a, nil
}

// ConfirmReturnLirAllocation mirrors the SQL: only status='return_requested'
// flips to 'returned'; arin_status='registered' co-promotes to
// 'removing' with attempt counters reset; other arin states stay.
func (f *fakeQ) ConfirmReturnLirAllocation(_ context.Context, arg dbq.ConfirmReturnLirAllocationParams) (dbq.LirAllocation, error) {
	a, ok := f.allocations[arg.ID]
	if !ok || a.Status != "return_requested" {
		return dbq.LirAllocation{}, pgx.ErrNoRows
	}
	a.Status = "returned"
	by := arg.ReturnedByUserID
	a.ReturnedByUserID = &by
	if a.ArinStatus == "registered" {
		a.ArinStatus = "removing"
		a.ArinAttempts = 0
		a.ArinLastAttemptAt = nil
		a.ArinLastError = nil
	}
	f.allocations[arg.ID] = a
	return a, nil
}

// ---- harness ----

func mount(f *fakeQ) http.Handler {
	r := chi.NewRouter()
	// Bypass the real auth middleware — every request gets a stub
	// principal holding "*". RequireCapability matches against that.
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := auth.WithPrincipal(r.Context(), auth.Principal{
				Capabilities: []string{"*"},
				Label:        "test",
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	(&Handler{Q: f}).Mount(r)
	return r
}

func do(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// ---- pool create ----

func TestCreatePool_Valid(t *testing.T) {
	f := newFake()
	rec := do(t, mount(f), "POST", "/lir/pools/", map[string]any{
		"name":              "DoW v4 NIPR",
		"slug":              "dow-v4-nipr",
		"ip_family":         4,
		"min_prefix_length": 20,
		"max_prefix_length": 29,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if f.created == nil || f.created.Name != "DoW v4 NIPR" {
		t.Errorf("create not recorded: %+v", f.created)
	}
}

func TestCreatePool_RejectsMissingNameSlug(t *testing.T) {
	rec := do(t, mount(newFake()), "POST", "/lir/pools/", map[string]any{
		"ip_family": 4, "min_prefix_length": 20, "max_prefix_length": 29,
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("got %d", rec.Code)
	}
}

func TestCreatePool_RejectsMinAboveMax(t *testing.T) {
	rec := do(t, mount(newFake()), "POST", "/lir/pools/", map[string]any{
		"name": "p", "slug": "p", "ip_family": 4,
		"min_prefix_length": 28, "max_prefix_length": 24,
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("got %d", rec.Code)
	}
}

func TestCreatePool_RejectsV4PrefixOver32(t *testing.T) {
	rec := do(t, mount(newFake()), "POST", "/lir/pools/", map[string]any{
		"name": "p", "slug": "p", "ip_family": 4,
		"min_prefix_length": 24, "max_prefix_length": 40,
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("got %d", rec.Code)
	}
}

func TestCreatePool_RejectsBadFamily(t *testing.T) {
	rec := do(t, mount(newFake()), "POST", "/lir/pools/", map[string]any{
		"name": "p", "slug": "p", "ip_family": 5,
		"min_prefix_length": 24, "max_prefix_length": 29,
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("got %d", rec.Code)
	}
}

// ---- pool get / list ----

func TestGetPool_NotFound(t *testing.T) {
	rec := do(t, mount(newFake()), "GET", "/lir/pools/"+uuid.New().String()+"/", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}

func TestGetPool_BadUUID(t *testing.T) {
	rec := do(t, mount(newFake()), "GET", "/lir/pools/not-a-uuid/", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d", rec.Code)
	}
}

func TestListPools_EmptyReturnsEmptyArray(t *testing.T) {
	rec := do(t, mount(newFake()), "GET", "/lir/pools/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	var body listPoolsResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Items == nil {
		t.Errorf("items should be [] not null on empty list")
	}
	if body.Total != 0 {
		t.Errorf("total: %d", body.Total)
	}
}

// ---- pool delete ----

func TestDeletePool_OK(t *testing.T) {
	f := newFake()
	id := uuid.New()
	f.pools[id] = dbq.LirPool{ID: id, IpFamily: 4, MinPrefixLength: 20, MaxPrefixLength: 29}
	rec := do(t, mount(f), "DELETE", "/lir/pools/"+id.String()+"/", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got %d", rec.Code)
	}
	if f.deleted == nil || *f.deleted != id {
		t.Errorf("delete not recorded: %+v", f.deleted)
	}
}

func TestDeletePool_ConflictsWhenAllocationsExist(t *testing.T) {
	f := newFake()
	id := uuid.New()
	f.pools[id] = dbq.LirPool{ID: id, IpFamily: 4, MinPrefixLength: 20, MaxPrefixLength: 29}
	f.allocCountByPool[id] = 3
	rec := do(t, mount(f), "DELETE", "/lir/pools/"+id.String()+"/", nil)
	if rec.Code != http.StatusConflict {
		t.Errorf("got %d", rec.Code)
	}
	if f.deleted != nil {
		t.Errorf("should not have deleted: %+v", f.deleted)
	}
}

// ---- attach / detach ----

func setupPoolAndSupernet(f *fakeQ, family int16) (uuid.UUID, uuid.UUID) {
	poolID := uuid.New()
	f.pools[poolID] = dbq.LirPool{
		ID: poolID, IpFamily: family, MinPrefixLength: 20, MaxPrefixLength: 29,
	}
	sn := uuid.New()
	prefix := "10.0.0.0/16"
	if family == 6 {
		prefix = "2001:db8::/32"
	}
	f.supernets[sn] = dbq.SupernetLirAttachRow{ID: sn, Prefix: prefix}
	return poolID, sn
}

func TestAttach_OK(t *testing.T) {
	f := newFake()
	poolID, sn := setupPoolAndSupernet(f, 4)
	rec := do(t, mount(f), "POST", "/lir/pools/"+poolID.String()+"/supernets/",
		map[string]any{"supernet_id": sn.String()})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if f.attached == nil || f.attached.ID != sn || f.attached.PoolID != poolID {
		t.Errorf("attach not recorded correctly: %+v", f.attached)
	}
}

func TestAttach_RejectsAlreadyPooled(t *testing.T) {
	f := newFake()
	poolID, sn := setupPoolAndSupernet(f, 4)
	otherPool := uuid.New()
	s := f.supernets[sn]
	s.LirPoolID = &otherPool
	f.supernets[sn] = s
	rec := do(t, mount(f), "POST", "/lir/pools/"+poolID.String()+"/supernets/",
		map[string]any{"supernet_id": sn.String()})
	if rec.Code != http.StatusConflict {
		t.Errorf("got %d", rec.Code)
	}
}

func TestAttach_RejectsTenantOwned(t *testing.T) {
	f := newFake()
	poolID, sn := setupPoolAndSupernet(f, 4)
	owner := uuid.New()
	s := f.supernets[sn]
	s.OwnerOrganizationID = &owner
	f.supernets[sn] = s
	rec := do(t, mount(f), "POST", "/lir/pools/"+poolID.String()+"/supernets/",
		map[string]any{"supernet_id": sn.String()})
	if rec.Code != http.StatusConflict {
		t.Errorf("got %d", rec.Code)
	}
}

func TestAttach_RejectsFamilyMismatch(t *testing.T) {
	f := newFake()
	// v6 pool, v4 supernet
	poolID, sn := setupPoolAndSupernet(f, 6)
	s := f.supernets[sn]
	s.Prefix = "10.0.0.0/16"
	f.supernets[sn] = s
	rec := do(t, mount(f), "POST", "/lir/pools/"+poolID.String()+"/supernets/",
		map[string]any{"supernet_id": sn.String()})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("got %d", rec.Code)
	}
}

func TestDetach_OK(t *testing.T) {
	f := newFake()
	poolID, sn := setupPoolAndSupernet(f, 4)
	s := f.supernets[sn]
	s.LirPoolID = &poolID
	f.supernets[sn] = s
	rec := do(t, mount(f), "DELETE",
		"/lir/pools/"+poolID.String()+"/supernets/"+sn.String(), nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if f.detached == nil || f.detached.ID != sn {
		t.Errorf("detach not recorded: %+v", f.detached)
	}
}

func TestDetach_NotAttachedToThisPool(t *testing.T) {
	f := newFake()
	poolID, sn := setupPoolAndSupernet(f, 4)
	other := uuid.New()
	s := f.supernets[sn]
	s.LirPoolID = &other
	f.supernets[sn] = s
	rec := do(t, mount(f), "DELETE",
		"/lir/pools/"+poolID.String()+"/supernets/"+sn.String(), nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}

func TestDetach_ConflictsWhenAllocationsExist(t *testing.T) {
	f := newFake()
	poolID, sn := setupPoolAndSupernet(f, 4)
	s := f.supernets[sn]
	s.LirPoolID = &poolID
	f.supernets[sn] = s
	f.allocCountByPoolSupernet[sn] = 2
	rec := do(t, mount(f), "DELETE",
		"/lir/pools/"+poolID.String()+"/supernets/"+sn.String(), nil)
	if rec.Code != http.StatusConflict {
		t.Errorf("got %d", rec.Code)
	}
}

// ---- pure validator unit tests ----

func TestSupernetFamily_V4(t *testing.T) {
	if supernetFamily("10.0.0.0/24") != 4 {
		t.Error("v4 prefix should yield 4")
	}
}

func TestSupernetFamily_V6(t *testing.T) {
	if supernetFamily("2001:db8::/48") != 6 {
		t.Error("v6 prefix should yield 6")
	}
}

func TestValidateAttachCandidate_OK(t *testing.T) {
	status, _ := validateAttachCandidate(
		dbq.LirPool{IpFamily: 4},
		dbq.SupernetLirAttachRow{Prefix: "10.0.0.0/16"},
	)
	if status != 0 {
		t.Errorf("clean case should return 0, got %d", status)
	}
}
