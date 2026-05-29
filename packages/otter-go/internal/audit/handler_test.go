package audit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

type fakeQ struct {
	last       dbq.ListAuditLogParams
	lastCount  dbq.CountAuditLogParams
	actions    []string
	lastActs   dbq.ListAuditActionsParams
	expandIDs  []uuid.UUID // what ListSiteIDsForExpansion returns
	expandErr  error
	expandArg  dbq.ListSiteIDsForExpansionParams
}

func (f *fakeQ) ListAuditLog(_ context.Context, a dbq.ListAuditLogParams) ([]dbq.AuditLog, error) {
	f.last = a
	return nil, nil
}
func (f *fakeQ) CountAuditLog(_ context.Context, a dbq.CountAuditLogParams) (int64, error) {
	f.lastCount = a
	return 0, nil
}
func (f *fakeQ) ListAuditActions(_ context.Context, a dbq.ListAuditActionsParams) ([]string, error) {
	f.lastActs = a
	return f.actions, nil
}
func (f *fakeQ) ListSiteIDsForExpansion(_ context.Context, a dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error) {
	f.expandArg = a
	return f.expandIDs, f.expandErr
}

// authedPrincipal carries audit:events:read with global scope so every
// existing test passes the cap-gate by default. Tests that exercise the
// gate (missing cap, scoped expansion) build their own principal.
func authedPrincipal() auth.Principal {
	return auth.Principal{
		Capabilities: []string{"audit:events:read"},
		Label:        "test",
	}
}

func mount(f *fakeQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}
func do(t *testing.T, h http.Handler, p string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", p, nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), authedPrincipal()))
	h.ServeHTTP(rec, req)
	return rec
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
	return auth.Principal{
		Capabilities: []string{"some:other:cap"},
		Label:        "test-nocap",
	}
}

// doWith fires a request with an arbitrary principal so individual tests
// can exercise scoped callers + missing-cap callers.
func doWith(t *testing.T, h http.Handler, p auth.Principal, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", path, nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	h.ServeHTTP(rec, req)
	return rec
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
	if len(f.last.ScopeSiteIds) != 2 {
		t.Fatalf("expected 2 scope_site_ids on List, got %d (%v)",
			len(f.last.ScopeSiteIds), f.last.ScopeSiteIds)
	}
	if len(f.lastCount.ScopeSiteIds) != 2 {
		t.Errorf("expected scope_site_ids forwarded on Count too, got %v",
			f.lastCount.ScopeSiteIds)
	}
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
	if f.last.Limit != 0 {
		t.Errorf("ListAuditLog should not have been called for empty-scope; got params %+v", f.last)
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
	if len(f.lastActs.ScopeSiteIds) != 1 || f.lastActs.ScopeSiteIds[0] != siteA {
		t.Errorf("expected scope_site_ids=[siteA], got %v", f.lastActs.ScopeSiteIds)
	}
}
