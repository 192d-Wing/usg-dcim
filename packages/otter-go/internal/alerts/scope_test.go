package alerts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

// PR 59 — alert_rules + maintenance_windows site-scope ABAC. Both
// resources carry a NULLABLE site (site_scope_id / site_id) where
// nil = "enterprise default" (applies to all sites) and only a global
// principal can mutate.

// scopedSiteFakeQ embeds fakeQ and overrides the PR 59 nullable-site
// lookups to return a configurable *uuid.UUID. site == nil simulates
// an enterprise-default rule; site == &x simulates a site-x rule.
type scopedSiteFakeQ struct {
	fakeQ
	site *uuid.UUID
}

func (s *scopedSiteFakeQ) GetAlertRuleSiteScopeID(_ context.Context, _ uuid.UUID) (*uuid.UUID, error) {
	return s.site, nil
}
func (s *scopedSiteFakeQ) GetMaintenanceWindowSiteID(_ context.Context, _ uuid.UUID) (*uuid.UUID, error) {
	return s.site, nil
}

func doAuthed(t *testing.T, q *scopedSiteFakeQ, p auth.Principal, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	(&Handler{Q: q}).Mount(r)
	var bodyReader *strings.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	var req *http.Request
	if bodyReader != nil {
		req = httptest.NewRequest(method, path, bodyReader)
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func scopedPrincipal(capCode string, siteIDs ...uuid.UUID) auth.Principal {
	set := make(map[uuid.UUID]struct{}, len(siteIDs))
	for _, id := range siteIDs {
		set[id] = struct{}{}
	}
	return auth.Principal{
		Capabilities: []string{capCode},
		Scopes:       map[string]auth.Scope{capCode: {SiteIDs: set}},
	}
}

// ---- AlertRule delete ----

func TestEnforceSite_RuleDelete_DifferentSite_Forbidden(t *testing.T) {
	owned, other := uuid.New(), uuid.New()
	q := &scopedSiteFakeQ{site: &other}
	rec := doAuthed(t, q, scopedPrincipal("alerts:rules:delete", owned),
		"DELETE", "/alerts/rules/"+uuid.New().String(), "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 (body=%q)", rec.Code, rec.Body.String())
	}
}

func TestEnforceSite_RuleDelete_SameSite_OK(t *testing.T) {
	owned := uuid.New()
	q := &scopedSiteFakeQ{site: &owned}
	rec := doAuthed(t, q, scopedPrincipal("alerts:rules:delete", owned),
		"DELETE", "/alerts/rules/"+uuid.New().String(), "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got %d, want 204 (body=%q)", rec.Code, rec.Body.String())
	}
}

// ---- Enterprise-default semantics ----

func TestEnforceSite_RuleDelete_EnterpriseDefault_ScopedForbidden(t *testing.T) {
	// site == nil → enterprise default. Scoped principal must be refused
	// even though they hold the cap with a non-empty scope.
	q := &scopedSiteFakeQ{site: nil}
	rec := doAuthed(t, q, scopedPrincipal("alerts:rules:delete", uuid.New()),
		"DELETE", "/alerts/rules/"+uuid.New().String(), "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 (body=%q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "enterprise-default") {
		t.Errorf("expected enterprise-default detail, body=%q", rec.Body.String())
	}
}

func TestEnforceSite_RuleDelete_EnterpriseDefault_GlobalOK(t *testing.T) {
	q := &scopedSiteFakeQ{site: nil}
	// Wildcard cap → global per FindScope contract.
	p := auth.Principal{Capabilities: []string{"*"}}
	rec := doAuthed(t, q, p, "DELETE", "/alerts/rules/"+uuid.New().String(), "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got %d, want 204 (body=%q)", rec.Code, rec.Body.String())
	}
}

// ---- AlertRule create: req.SiteScopeID ----

func TestEnforceSite_RuleCreate_BodySite_Forbidden(t *testing.T) {
	owned := uuid.New()
	q := &scopedSiteFakeQ{}
	body := `{"name":"r","metric":"m","operator":">","threshold":1.0,"severity":"warning","site_scope_id":"` + uuid.New().String() + `"}`
	rec := doAuthed(t, q, scopedPrincipal("alerts:rules:create", owned),
		"POST", "/alerts/rules", body)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 (body=%q)", rec.Code, rec.Body.String())
	}
}

func TestEnforceSite_RuleCreate_EnterpriseDefault_ScopedForbidden(t *testing.T) {
	// Scoped principal trying to create an enterprise-default rule
	// (site_scope_id omitted from body → nil).
	q := &scopedSiteFakeQ{}
	body := `{"name":"r","metric":"m","operator":">","threshold":1.0,"severity":"warning"}`
	rec := doAuthed(t, q, scopedPrincipal("alerts:rules:create", uuid.New()),
		"POST", "/alerts/rules", body)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 (body=%q)", rec.Code, rec.Body.String())
	}
}

// ---- AlertRule update: reassignment check ----

func TestEnforceSite_RuleUpdate_ReassignToForbiddenSite(t *testing.T) {
	// Principal owns site A. Rule is currently in site A (so the
	// current-site check passes), but the body reassigns to site B
	// which the principal does NOT own — must be refused.
	owned := uuid.New()
	q := &scopedSiteFakeQ{site: &owned}
	other := uuid.New()
	body := `{"site_scope_id":"` + other.String() + `"}`
	rec := doAuthed(t, q, scopedPrincipal("alerts:rules:update", owned),
		"PATCH", "/alerts/rules/"+uuid.New().String(), body)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 (body=%q)", rec.Code, rec.Body.String())
	}
}

func TestEnforceSite_RuleUpdate_ReassignToEnterpriseDefault_ScopedForbidden(t *testing.T) {
	// Scoped principal trying to elevate a site rule into an
	// enterprise-default rule (site_scope_id explicitly null) — must
	// be refused.
	owned := uuid.New()
	q := &scopedSiteFakeQ{site: &owned}
	body := `{"site_scope_id":null}`
	rec := doAuthed(t, q, scopedPrincipal("alerts:rules:update", owned),
		"PATCH", "/alerts/rules/"+uuid.New().String(), body)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 (body=%q)", rec.Code, rec.Body.String())
	}
}

// ---- MaintenanceWindow — same shape, table-driven smoke test ----

func TestEnforceSite_MaintenanceWindowDeletes(t *testing.T) {
	owned, other := uuid.New(), uuid.New()
	cases := []struct {
		name     string
		site     *uuid.UUID
		p        auth.Principal
		wantCode int
	}{
		{"different-site forbidden", &other,
			scopedPrincipal("alerts:maintenance-windows:delete", owned), http.StatusForbidden},
		{"same-site ok", &owned,
			scopedPrincipal("alerts:maintenance-windows:delete", owned), http.StatusNoContent},
		{"enterprise-default scoped forbidden", nil,
			scopedPrincipal("alerts:maintenance-windows:delete", owned), http.StatusForbidden},
		{"enterprise-default global ok", nil,
			auth.Principal{Capabilities: []string{"*"}}, http.StatusNoContent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &scopedSiteFakeQ{site: tc.site}
			rec := doAuthed(t, q, tc.p, "DELETE",
				"/alerts/maintenance-windows/"+uuid.New().String(), "")
			if rec.Code != tc.wantCode {
				t.Fatalf("got %d, want %d (body=%q)", rec.Code, tc.wantCode, rec.Body.String())
			}
		})
	}
}
