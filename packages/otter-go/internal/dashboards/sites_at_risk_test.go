package dashboards

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

// fakeArQ stubs only the SitesAtRisk slice. Embeds fakeQ so the
// enterprise + free-space surfaces are still satisfied — the embedded
// methods are unused here.
type fakeArQ struct {
	fakeQ
	rows        []dbq.SiteAtRiskRow
	gotSeverity string
	listErr     error
}

func (f *fakeArQ) ListSitesAtRisk(_ context.Context, sev string) ([]dbq.SiteAtRiskRow, error) {
	f.gotSeverity = sev
	return f.rows, f.listErr
}

func mountAr(f *fakeArQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f, CollectorStaleSeconds: 600}).Mount(r)
	return r
}

func TestSitesAtRisk_DefaultSeverityIsMajor(t *testing.T) {
	f := &fakeArQ{rows: []dbq.SiteAtRiskRow{{SiteID: uuid.New(), AlertCount: 3}}}
	rec := authtest.ServeRequest(
		mountAr(f),
		authtest.PrincipalWithCaps(capDashboardsRead),
		"GET", "/dashboards/sites/at-risk", nil,
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if f.gotSeverity != "major" {
		t.Errorf("default severity = %q, want major", f.gotSeverity)
	}
	var body sitesAtRiskResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Sites) != 1 || body.Sites[0].AlertCount != 3 {
		t.Errorf("wrong body: %+v", body)
	}
}

// Empty result still emits a non-nil `sites: []` so finch's
// `data.sites.map(...)` doesn't NPE.
func TestSitesAtRisk_EmptyEncodesAsArray(t *testing.T) {
	f := &fakeArQ{}
	rec := authtest.ServeRequest(
		mountAr(f),
		authtest.PrincipalWithCaps(capDashboardsRead),
		"GET", "/dashboards/sites/at-risk", nil,
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"sites":[]`) {
		t.Errorf("empty result should encode as [], got %s", rec.Body.String())
	}
}

func TestSitesAtRisk_SeverityParamThreaded(t *testing.T) {
	for _, sev := range []string{"info", "warning", "minor", "major", "critical"} {
		f := &fakeArQ{}
		authtest.ServeRequest(
			mountAr(f),
			authtest.PrincipalWithCaps(capDashboardsRead),
			"GET", "/dashboards/sites/at-risk?severity="+sev, nil,
		)
		if f.gotSeverity != sev {
			t.Errorf("severity = %q, want %q", f.gotSeverity, sev)
		}
	}
}

// Invalid severity values 400. SQL-injection bait gets rejected
// before reaching the SQL layer.
func TestSitesAtRisk_InvalidSeverityIs400(t *testing.T) {
	for _, bad := range []string{"sev", "high", "INFO", "', DROP TABLE"} {
		rec := authtest.ServeRequest(
			mountAr(&fakeArQ{}),
			authtest.PrincipalWithCaps(capDashboardsRead),
			"GET", "/dashboards/sites/at-risk?severity="+url.QueryEscape(bad), nil,
		)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("severity=%q should 400; got %d", bad, rec.Code)
		}
	}
}

func TestSitesAtRisk_SiteIDRenderedAsString(t *testing.T) {
	id := uuid.New()
	f := &fakeArQ{rows: []dbq.SiteAtRiskRow{{SiteID: id, AlertCount: 5}}}
	rec := authtest.ServeRequest(
		mountAr(f),
		authtest.PrincipalWithCaps(capDashboardsRead),
		"GET", "/dashboards/sites/at-risk", nil,
	)
	var body sitesAtRiskResponse
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if len(body.Sites) != 1 || body.Sites[0].SiteID != id.String() {
		t.Errorf("site_id should render as %q; got %+v", id, body.Sites)
	}
}

func TestSitesAtRisk_RejectsWithoutCap(t *testing.T) {
	rec := authtest.ServeRequest(
		mountAr(&fakeArQ{}),
		authtest.PrincipalWithCaps("inventory:sites:read"),
		"GET", "/dashboards/sites/at-risk", nil,
	)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestSitesAtRisk_DBErrorIs500(t *testing.T) {
	f := &fakeArQ{listErr: errFake}
	rec := authtest.ServeRequest(
		mountAr(f),
		authtest.PrincipalWithCaps(capDashboardsRead),
		"GET", "/dashboards/sites/at-risk", nil,
	)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}
