package alerts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

// PR 63 — site-scope LIST filtering for alerts / alert_rules /
// maintenance_windows. alerts.site_id is NOT NULL (short-circuit on
// empty scope), but alert_rules.site_scope_id and
// maintenance_windows.site_id are nullable — enterprise defaults
// remain visible even when the scope expands to an empty set.

// expansionFakeQ embeds fakeQ and lets a test seed the expansion
// result returned to ScopedSiteFilter.
type expansionFakeQ struct {
	fakeQ
	expandResult []uuid.UUID
}

func (e *expansionFakeQ) ListSiteIDsForExpansion(_ context.Context, _ dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error) {
	return e.expandResult, nil
}

func mountWith(q Querier) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: q}).Mount(r)
	return r
}

func TestAlertsList_ScopedPrincipal_ThreadsSiteIds(t *testing.T) {
	owned := uuid.New()
	q := &expansionFakeQ{expandResult: []uuid.UUID{owned}}
	p := auth.Principal{
		Capabilities: []string{"alerts:alerts:read"},
		Scopes: map[string]auth.Scope{
			"alerts:alerts:read": {SiteIDs: map[uuid.UUID]struct{}{owned: {}}},
		},
	}
	req := httptest.NewRequest("GET", "/alerts", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountWith(q).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}
	if len(q.lastA.ScopeSiteIds) != 1 || q.lastA.ScopeSiteIds[0] != owned {
		t.Errorf("ListAlerts ScopeSiteIds: want [%s], got %v", owned, q.lastA.ScopeSiteIds)
	}
}

func TestAlertsList_ScopedEmpty_ShortCircuits(t *testing.T) {
	// alerts.site_id is NOT NULL — scoped+empty means no rows reachable.
	q := &expansionFakeQ{expandResult: nil}
	p := auth.Principal{
		Capabilities: []string{"alerts:alerts:read"},
		Scopes: map[string]auth.Scope{
			"alerts:alerts:read": {FabricIDs: map[uuid.UUID]struct{}{uuid.New(): {}}},
		},
	}
	req := httptest.NewRequest("GET", "/alerts", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountWith(q).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	if q.lastA.ScopeSiteIds != nil {
		t.Errorf("ListAlerts should not be called when scope set is empty; captured params=%+v", q.lastA)
	}
}

func TestAlertRulesList_ScopedEmpty_StillCallsDB(t *testing.T) {
	// alert_rules.site_scope_id is NULLABLE — enterprise defaults must
	// remain visible to scoped principals. Handler must NOT
	// short-circuit on empty allowed set; the SQL's IS NULL clause
	// surfaces the enterprise defaults.
	q := &expansionFakeQ{expandResult: []uuid.UUID{}}
	p := auth.Principal{
		Capabilities: []string{"alerts:rules:read"},
		Scopes: map[string]auth.Scope{
			"alerts:rules:read": {FabricIDs: map[uuid.UUID]struct{}{uuid.New(): {}}},
		},
	}
	req := httptest.NewRequest("GET", "/alerts/rules", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountWith(q).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}
	// ListAlertRules must have been called (no short-circuit). The
	// captured Params is the zero ListAlertRulesParams when not called,
	// but the handler always sets Limit + Offset + ScopeSiteIds. So
	// limit being set tells us the handler ran the query.
	if q.lastR.Limit == 0 {
		t.Errorf("ListAlertRules should have been called even for empty scope set; got %+v", q.lastR)
	}
	if q.lastR.ScopeSiteIds == nil {
		t.Errorf("ScopeSiteIds should be a non-nil empty slice, got nil")
	}
	if len(q.lastR.ScopeSiteIds) != 0 {
		t.Errorf("ScopeSiteIds should be empty slice, got %v", q.lastR.ScopeSiteIds)
	}
}

func TestMaintenanceWindowsList_ScopedEmpty_StillCallsDB(t *testing.T) {
	// Same nullable semantic as alert_rules.
	q := &expansionFakeQ{expandResult: []uuid.UUID{}}
	p := auth.Principal{
		Capabilities: []string{"alerts:maintenance-windows:read"},
		Scopes: map[string]auth.Scope{
			"alerts:maintenance-windows:read": {FabricIDs: map[uuid.UUID]struct{}{uuid.New(): {}}},
		},
	}
	req := httptest.NewRequest("GET", "/alerts/maintenance-windows", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountWith(q).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}
	if q.lastMW.Limit == 0 {
		t.Errorf("ListMaintenanceWindows should have been called even for empty scope set")
	}
	if q.lastMW.ScopeSiteIds == nil {
		t.Errorf("ScopeSiteIds should be non-nil empty slice, got nil")
	}
}

func TestAlertsList_Global_NoFilter(t *testing.T) {
	q := &expansionFakeQ{}
	p := auth.Principal{Capabilities: []string{"*"}}
	req := httptest.NewRequest("GET", "/alerts", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountWith(q).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	if q.lastA.ScopeSiteIds != nil {
		t.Errorf("global principal should pass nil ScopeSiteIds, got %v", q.lastA.ScopeSiteIds)
	}
}
