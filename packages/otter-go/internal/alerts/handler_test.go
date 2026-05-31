package alerts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

type fakeQ struct {
	lastA  dbq.ListAlertsParams
	lastR  dbq.ListAlertRulesParams
	lastMW dbq.ListMaintenanceWindowsParams
}

func (f *fakeQ) ListAlerts(_ context.Context, a dbq.ListAlertsParams) ([]dbq.Alert, error) {
	f.lastA = a
	return nil, nil
}
func (f *fakeQ) CountAlerts(_ context.Context, _ dbq.CountAlertsParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) ListAlertRules(_ context.Context, a dbq.ListAlertRulesParams) ([]dbq.AlertRule, error) {
	f.lastR = a
	return nil, nil
}
func (f *fakeQ) CountAlertRules(_ context.Context, _ dbq.CountAlertRulesParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) GetAlertRule(_ context.Context, _ uuid.UUID) (dbq.AlertRule, error) {
	return dbq.AlertRule{}, pgx.ErrNoRows
}

// Maintenance-window stubs. The "last" capture is on the params so a
// test can verify filter threading; the rest of fakeQ keeps the same
// nil-returning pattern as the alert stubs.
func (f *fakeQ) ListMaintenanceWindows(_ context.Context, a dbq.ListMaintenanceWindowsParams) ([]dbq.MaintenanceWindow, error) {
	f.lastMW = a
	return nil, nil
}
func (f *fakeQ) CountMaintenanceWindows(_ context.Context, _ dbq.CountMaintenanceWindowsParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) GetMaintenanceWindow(_ context.Context, _ uuid.UUID) (dbq.MaintenanceWindow, error) {
	return dbq.MaintenanceWindow{}, pgx.ErrNoRows
}

// ---- Mutation stubs (PR 45) ----
func (f *fakeQ) AckAlert(_ context.Context, a dbq.AckAlertParams) (dbq.Alert, error) {
	return dbq.Alert{ID: a.ID, State: "acked", AckedBy: &a.AckedBy}, nil
}
func (f *fakeQ) CreateAlertRule(_ context.Context, a dbq.CreateAlertRuleParams) (dbq.AlertRule, error) {
	return dbq.AlertRule{ID: uuid.New(), Name: a.Name, Metric: a.Metric, Operator: a.Operator, Threshold: a.Threshold, Severity: a.Severity}, nil
}
func (f *fakeQ) UpdateAlertRule(_ context.Context, a dbq.UpdateAlertRuleParams) (dbq.AlertRule, error) {
	return dbq.AlertRule{ID: a.ID}, nil
}
func (f *fakeQ) DeleteAlertRule(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) CreateMaintenanceWindow(_ context.Context, a dbq.CreateMaintenanceWindowParams) (dbq.MaintenanceWindow, error) {
	return dbq.MaintenanceWindow{ID: uuid.New(), Name: a.Name, StartsAt: a.StartsAt, EndsAt: a.EndsAt}, nil
}
func (f *fakeQ) UpdateMaintenanceWindow(_ context.Context, a dbq.UpdateMaintenanceWindowParams) (dbq.MaintenanceWindow, error) {
	return dbq.MaintenanceWindow{ID: a.ID}, nil
}
func (f *fakeQ) DeleteMaintenanceWindow(_ context.Context, _ uuid.UUID) error { return nil }

// PR 59 — nullable-site-scope lookups + site-scope expansion. Tests
// that don't care about scope let these return nil/uuid.Nil; the
// nullable case (nil site) means "enterprise default" — only global
// principals can mutate.
func (f *fakeQ) GetAlertRuleSiteScopeID(_ context.Context, _ uuid.UUID) (*uuid.UUID, error) {
	return nil, nil
}
func (f *fakeQ) GetMaintenanceWindowSiteID(_ context.Context, _ uuid.UUID) (*uuid.UUID, error) {
	return nil, nil
}
func (f *fakeQ) GetSiteRegionID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *fakeQ) GetSiteOrganizationID(_ context.Context, _ uuid.UUID) (*uuid.UUID, error) {
	return nil, nil
}
func (f *fakeQ) ListSiteGroupIDsForSite(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

// PR 63 — site-scope expansion. Default returns nil (no expansion).
func (f *fakeQ) ListSiteIDsForExpansion(_ context.Context, _ dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error) {
	return nil, nil
}

func mount(f *fakeQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}
func do(t *testing.T, h http.Handler, p string) *httptest.ResponseRecorder {
	t.Helper()
	// Wildcard principal — alerts GETs require the matching
	// alerts:X:read / maintenance:windows:read cap (PR cap-gate
	// hardening). Per-cap denial paths are exercised separately
	// in caps_test.go.
	req := authtest.Request("GET", p, authtest.PrincipalWithCaps("*"), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestListAlerts_Filters(t *testing.T) {
	sid := uuid.New()
	f := &fakeQ{}
	do(t, mount(f), "/alerts?site_id="+sid.String()+"&state=firing&severity=major")
	if f.lastA.SiteID == nil || *f.lastA.SiteID != sid {
		t.Error("site_id")
	}
	if f.lastA.State == nil || *f.lastA.State != "firing" {
		t.Error("state")
	}
	if f.lastA.Severity == nil || *f.lastA.Severity != "major" {
		t.Error("severity")
	}
}

func TestListRules_Filters(t *testing.T) {
	sid := uuid.New()
	f := &fakeQ{}
	do(t, mount(f), "/alerts/rules?site_scope_id="+sid.String()+"&enabled=true")
	if f.lastR.SiteScopeID == nil || *f.lastR.SiteScopeID != sid {
		t.Error("site_scope_id")
	}
	if f.lastR.Enabled == nil || !*f.lastR.Enabled {
		t.Error("enabled=true")
	}
}

func TestBadUUIDs(t *testing.T) {
	for _, p := range []string{"/alerts?site_id=x", "/alerts/rules?site_scope_id=x"} {
		rec := do(t, mount(&fakeQ{}), p)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d", p, rec.Code)
		}
	}
}

func TestListAlerts_TrailingSlashAlsoWorks(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/alerts/")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestListRules_PageSizeAlias(t *testing.T) {
	f := &fakeQ{}
	rec := do(t, mount(f), "/alerts/rules?page_size=200")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	if f.lastR.Limit != 200 {
		t.Errorf("page_size not honored: %d", f.lastR.Limit)
	}
}

func TestGetRule_NotFound(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/alerts/rules/"+uuid.New().String())
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}

func TestGetRule_BadID(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/alerts/rules/not-uuid")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d", rec.Code)
	}
}

func TestListMaintenanceWindows_AllFilters(t *testing.T) {
	sid := uuid.New()
	f := &fakeQ{}
	rec := do(t, mount(f),
		"/alerts/maintenance-windows?site_id="+sid.String()+
			"&active_at=2025-06-01T12:00:00Z&upcoming=true")
	if rec.Code != 200 {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if f.lastMW.SiteID == nil || *f.lastMW.SiteID != sid {
		t.Error("site_id not threaded")
	}
	if f.lastMW.ActiveAt == nil {
		t.Error("active_at not parsed")
	}
	if f.lastMW.UpcomingAfter == nil {
		t.Error("upcoming=true should translate to UpcomingAfter")
	}
}

func TestListMaintenanceWindows_BadActiveAt(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/alerts/maintenance-windows?active_at=not-a-time")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d", rec.Code)
	}
}

func TestGetMaintenanceWindow_NotFound(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/alerts/maintenance-windows/"+uuid.New().String())
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}
