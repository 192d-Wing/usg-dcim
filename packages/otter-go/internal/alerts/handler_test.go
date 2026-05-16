package alerts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

type fakeQ struct {
	lastA dbq.ListAlertsParams
	lastR dbq.ListAlertRulesParams
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

func mount(f *fakeQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}
func do(t *testing.T, h http.Handler, p string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
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
