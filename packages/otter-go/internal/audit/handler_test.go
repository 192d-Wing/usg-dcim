package audit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

type fakeQ struct {
	last      dbq.ListAuditLogParams
	lastCount dbq.CountAuditLogParams
	actions   []string
	lastActs  dbq.ListAuditActionsParams
	expandIDs []uuid.UUID // what ListSiteIDsForExpansion returns
	expandErr error
	expandArg dbq.ListSiteIDsForExpansionParams

	// Explicit call sentinels — replace earlier `f.last.Limit != 0`
	// proxy assertions that coupled the test to incidental param
	// defaults. listLogCalled / listActionsCalled flip on entry so a
	// regression that calls the underlying query with a zero-value
	// params struct can still be caught.
	listLogCalled     bool
	listActionsCalled bool
	expandCalled      bool
}

func (f *fakeQ) ListAuditLog(_ context.Context, a dbq.ListAuditLogParams) ([]dbq.AuditLog, error) {
	f.listLogCalled = true
	f.last = a
	return nil, nil
}
func (f *fakeQ) CountAuditLog(_ context.Context, a dbq.CountAuditLogParams) (int64, error) {
	f.lastCount = a
	return 0, nil
}
func (f *fakeQ) ListAuditActions(_ context.Context, a dbq.ListAuditActionsParams) ([]string, error) {
	f.listActionsCalled = true
	f.lastActs = a
	return f.actions, nil
}
func (f *fakeQ) ListSiteIDsForExpansion(_ context.Context, a dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error) {
	f.expandCalled = true
	f.expandArg = a
	return f.expandIDs, f.expandErr
}

// authedPrincipal carries audit:events:read with global scope so every
// existing test passes the cap-gate by default. Tests that exercise the
// gate (missing cap, scoped expansion) build their own principal.
//
// Thin wrapper over the shared authtest helper so call sites in this
// package read naturally; new handler packages should call
// authtest.PrincipalWithCaps directly.
func authedPrincipal() auth.Principal {
	return authtest.PrincipalWithCaps("audit:events:read")
}

func mount(f *fakeQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}
func do(t *testing.T, h http.Handler, p string) *httptest.ResponseRecorder {
	t.Helper()
	return authtest.ServeRequest(h, authedPrincipal(), "GET", p, nil)
}

func TestListLog_AllFilters(t *testing.T) {
	uid, sid := uuid.New(), uuid.New()
	f := &fakeQ{}
	url := "/audit/log?actor_user_id=" + uid.String() +
		"&action=asset.update&target_type=asset&target_id=abc&site_id=" + sid.String() +
		"&since=2025-01-01T00:00:00Z&until=2025-12-31T23:59:59Z&success=false"
	rec := do(t, mount(f), url)
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if f.last.ActorUserID == nil || *f.last.ActorUserID != uid {
		t.Error("actor_user_id")
	}
	if f.last.Action == nil || *f.last.Action != "asset.update" {
		t.Error("action")
	}
	if f.last.SiteID == nil || *f.last.SiteID != sid {
		t.Error("site_id")
	}
	if f.last.Since == nil {
		t.Error("since not parsed")
	}
	if f.last.Until == nil {
		t.Error("until not parsed")
	}
	if f.last.Success == nil || *f.last.Success {
		t.Error("success=false not threaded")
	}
}

func TestListLog_TargetIDsAndPageSize(t *testing.T) {
	f := &fakeQ{}
	rec := do(t, mount(f), "/audit/log?target_ids=a,b,a&page_size=200")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	if f.last.Limit != 200 {
		t.Errorf("page_size not honored: %d", f.last.Limit)
	}
	if got := f.last.TargetIDs; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("target_ids not parsed/deduped: %#v", got)
	}
}

// TestListLog_PageParamComputesOffset verifies the Refine-flavored
// pagination contract: `?page=N&page_size=M` should land on the
// underlying query as Limit=M, Offset=(N-1)*M. Without this, finch's
// audit-log table renders page 1 forever — clicking "Next" issues
// ?page=2 which the server used to silently discard.
func TestListLog_PageParamComputesOffset(t *testing.T) {
	f := &fakeQ{}
	rec := do(t, mount(f), "/audit/log?page=3&page_size=20")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	if f.last.Limit != 20 {
		t.Errorf("limit = %d, want 20", f.last.Limit)
	}
	if f.last.Offset != 40 {
		t.Errorf("offset = %d, want 40 (page=3, page_size=20)", f.last.Offset)
	}
}

// TestListLog_OffsetOverridesPage pins the precedence: an explicit
// ?offset wins over ?page so API-token callers and existing curl/script
// integrations keep working when finch starts sending both.
func TestListLog_OffsetOverridesPage(t *testing.T) {
	f := &fakeQ{}
	rec := do(t, mount(f), "/audit/log?page=3&page_size=20&offset=5")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	if f.last.Offset != 5 {
		t.Errorf("explicit ?offset should win; got offset=%d", f.last.Offset)
	}
}

func TestListLog_BadSince(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/audit/log?since=not-a-time")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d", rec.Code)
	}
}

func TestListActions_EmptyReturnsArray(t *testing.T) {
	rec := do(t, mount(&fakeQ{actions: nil}), "/audit/actions")
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	var got []string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Error("should be empty array, not null")
	}
}

// ---- cap-gate + ABAC ----

// noCapPrincipal lets us assert RequireCapability("audit:events:read")
// rejects callers without the cap, regardless of authentication.
func noCapPrincipal() auth.Principal {
	return authtest.PrincipalWithCaps("some:other:cap")
}

// doWith fires a request with an arbitrary principal so individual tests
// can exercise scoped callers + missing-cap callers.
func doWith(t *testing.T, h http.Handler, p auth.Principal, path string) *httptest.ResponseRecorder {
	t.Helper()
	return authtest.ServeRequest(h, p, "GET", path, nil)
}

func TestListLog_RejectsWithoutCap(t *testing.T) {
	rec := doWith(t, mount(&fakeQ{}), noCapPrincipal(), "/audit/log")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (missing audit:events:read)", rec.Code)
	}
}

func TestListActions_RejectsWithoutCap(t *testing.T) {
	rec := doWith(t, mount(&fakeQ{}), noCapPrincipal(), "/audit/actions")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (missing audit:events:read)", rec.Code)
	}
}

// Site-scoped principal: ScopedSiteFilter expands the scope into a
// concrete site_id slice via ListSiteIDsForExpansion. The handler
// threads that slice into ListAuditLogParams.ScopeSiteIds so the SQL
// `site_id = ANY($scope_site_ids)` filter restricts the result.
func TestListLog_ScopedThreadsSiteIDs(t *testing.T) {
	siteA, siteB := uuid.New(), uuid.New()
	f := &fakeQ{expandIDs: []uuid.UUID{siteA, siteB}}
	p := auth.Principal{
		Capabilities: []string{"audit:events:read"},
		Scopes: map[string]auth.Scope{
			"audit:events:read": {
				SiteIDs: map[uuid.UUID]struct{}{siteA: {}, siteB: {}},
			},
		},
	}
	rec := doWith(t, mount(f), p, "/audit/log")
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	// Assert membership, not just length. A regression that
	// threaded the principal's RegionIDs (also a UUID slice of
	// length 2) into ScopeSiteIds would have passed the prior
	// len()==2 check.
	if !uuidSetEqual(f.last.ScopeSiteIds, []uuid.UUID{siteA, siteB}) {
		t.Fatalf("ListAuditLog.ScopeSiteIds = %v, want set {siteA,siteB}", f.last.ScopeSiteIds)
	}
	if !uuidSetEqual(f.lastCount.ScopeSiteIds, []uuid.UUID{siteA, siteB}) {
		t.Errorf("CountAuditLog.ScopeSiteIds = %v, want set {siteA,siteB}", f.lastCount.ScopeSiteIds)
	}
	// And the principal's direct SiteIDs must have been forwarded
	// to ScopedSiteFilter's expansion query — guards against a
	// regression that passes a different scope dimension by mistake.
	if !uuidSetEqual(f.expandArg.DirectSiteIds, []uuid.UUID{siteA, siteB}) {
		t.Errorf("ListSiteIDsForExpansion.DirectSiteIds = %v, want set {siteA,siteB}", f.expandArg.DirectSiteIds)
	}
}

// uuidSetEqual reports whether the two slices contain the same set
// of UUIDs (order-independent, duplicates collapsed). Pure helper.
func uuidSetEqual(a, b []uuid.UUID) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[uuid.UUID]struct{}, len(a))
	for _, id := range a {
		seen[id] = struct{}{}
	}
	for _, id := range b {
		if _, ok := seen[id]; !ok {
			return false
		}
	}
	return true
}

// Scoped principal whose dimensions don't expand to any site (e.g.
// enclave-only scope) should short-circuit to an empty page WITHOUT
// hitting the underlying ListAuditLog query — mirrors Python
// `if not in_scope: return empty_page(...)`.
func TestListLog_ScopedEmptyExpansionShortCircuits(t *testing.T) {
	f := &fakeQ{expandIDs: nil}
	p := auth.Principal{
		Capabilities: []string{"audit:events:read"},
		Scopes: map[string]auth.Scope{
			"audit:events:read": {
				// Non-site dimension only — expands to []
				Enclaves: map[string]struct{}{"unclass": {}},
			},
		},
	}
	rec := doWith(t, mount(f), p, "/audit/log")
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	// Explicit call sentinel — was previously `f.last.Limit != 0`,
	// which would have falsely passed if a regression invoked
	// ListAuditLog with a zero-value Params{}.
	if f.listLogCalled {
		t.Errorf("ListAuditLog should not have been called for empty-scope; params=%+v", f.last)
	}
	// Body should be an empty page, not null.
	var body struct {
		Items []dbq.AuditLog `json:"items"`
		Total int64          `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Items == nil {
		t.Error("items should be empty array, not null")
	}
}

func TestListActions_ScopedThreadsSiteIDs(t *testing.T) {
	siteA := uuid.New()
	f := &fakeQ{expandIDs: []uuid.UUID{siteA}, actions: []string{"asset.update"}}
	p := auth.Principal{
		Capabilities: []string{"audit:events:read"},
		Scopes: map[string]auth.Scope{
			"audit:events:read": {SiteIDs: map[uuid.UUID]struct{}{siteA: {}}},
		},
	}
	rec := doWith(t, mount(f), p, "/audit/actions")
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if !uuidSetEqual(f.lastActs.ScopeSiteIds, []uuid.UUID{siteA}) {
		t.Errorf("expected scope_site_ids=[siteA], got %v", f.lastActs.ScopeSiteIds)
	}
	if !uuidSetEqual(f.expandArg.DirectSiteIds, []uuid.UUID{siteA}) {
		t.Errorf("expansion DirectSiteIds = %v, want [siteA]", f.expandArg.DirectSiteIds)
	}
}

// ---- defense-in-depth + scope-enforce + sentinel-free target_ids ----

// TestListLog_MissingPrincipal500s verifies the defense-in-depth
// guard: if a request somehow reaches listLog without a Principal in
// context (i.e. RequireCapability was bypassed), we 500 instead of
// silently elevating to the global path that would dump the entire
// audit_log.
func TestListLog_MissingPrincipal500s(t *testing.T) {
	rec := httptest.NewRecorder()
	// No auth.WithPrincipal call — context carries no principal.
	req := httptest.NewRequest("GET", "/audit/log", nil)
	mount(&fakeQ{}).ServeHTTP(rec, req)
	// The cap-gate middleware actually 500s with "missing principal"
	// before reaching the handler, so this exercises the FIRST layer
	// of the defense (RequireCapability). The handler's own !ok guard
	// is the SECOND layer; both produce 500.
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// TestListLog_SiteIDOutsideScopeIs403 verifies the scope enforcement
// on the user-supplied `?site_id=` query param. Without this check,
// a scoped caller probing for audit data on a site they don't own
// would get 200/empty (silently masking an authz failure).
func TestListLog_SiteIDOutsideScopeIs403(t *testing.T) {
	siteA, siteB := uuid.New(), uuid.New() // siteB is OUT of scope
	f := &fakeQ{expandIDs: []uuid.UUID{siteA}}
	p := auth.Principal{
		Capabilities: []string{capRead},
		Scopes: map[string]auth.Scope{
			capRead: {SiteIDs: map[uuid.UUID]struct{}{siteA: {}}},
		},
	}
	rec := doWith(t, mount(f), p, "/audit/log?site_id="+siteB.String())
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (siteB outside scope)", rec.Code)
	}
	if f.last.SiteID != nil {
		t.Error("ListAuditLog should not have been called with out-of-scope site_id")
	}
}

// TestListLog_SiteIDInsideScopeIsAllowed is the positive case: when
// the caller passes a site_id that IS in their reachable expansion,
// the request proceeds normally and the SQL filter includes both
// the explicit site_id and the scope_site_ids predicate.
func TestListLog_SiteIDInsideScopeIsAllowed(t *testing.T) {
	siteA := uuid.New()
	f := &fakeQ{expandIDs: []uuid.UUID{siteA}}
	p := auth.Principal{
		Capabilities: []string{capRead},
		Scopes: map[string]auth.Scope{
			capRead: {SiteIDs: map[uuid.UUID]struct{}{siteA: {}}},
		},
	}
	rec := doWith(t, mount(f), p, "/audit/log?site_id="+siteA.String())
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if f.last.SiteID == nil || *f.last.SiteID != siteA {
		t.Errorf("site_id not threaded into ListAuditLog: %+v", f.last.SiteID)
	}
}

// TestListLog_GlobalCallerAnySiteAllowed checks that a global caller
// (no Scopes map) can pass any site_id without 403 — siteInScope
// returns true when scopeSiteIDs is nil.
func TestListLog_GlobalCallerAnySiteAllowed(t *testing.T) {
	siteX := uuid.New()
	f := &fakeQ{}
	rec := do(t, mount(f), "/audit/log?site_id="+siteX.String())
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if f.last.SiteID == nil || *f.last.SiteID != siteX {
		t.Errorf("site_id not threaded for global caller")
	}
}

// TestListLog_ExplicitEmptyTargetIDs verifies the splitCSV signal
// path: `?target_ids=,` means "match nothing" and is now expressed as
// an empty slice ([]string{}) rather than the `__dcim_no_target_ids__`
// sentinel that could collide with a legitimate target_id.
func TestListLog_ExplicitEmptyTargetIDs(t *testing.T) {
	f := &fakeQ{}
	rec := do(t, mount(f), "/audit/log?target_ids=,")
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if f.last.TargetIDs == nil {
		t.Fatal("TargetIDs should be a non-nil empty slice, got nil")
	}
	if len(f.last.TargetIDs) != 0 {
		t.Errorf("TargetIDs should be empty, got %v", f.last.TargetIDs)
	}
	for _, id := range f.last.TargetIDs {
		if strings.Contains(id, "__dcim") {
			t.Errorf("sentinel string leaked into SQL filter: %q", id)
		}
	}
}
