package alerts

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

// Each alerts GET route requires its matching read cap after this PR.
// A principal holding a different alerts-namespace read cap must not
// satisfy it. Mirrors the BGP caps_test pattern from PR #205.
func TestAlerts_GETsRequireMatchingReadCap(t *testing.T) {
	cases := []struct {
		path   string
		cap    string
		others []string
	}{
		{"/alerts", "alerts:alerts:read", []string{"alerts:rules:read"}},
		{"/alerts/rules", "alerts:rules:read", []string{"alerts:alerts:read"}},
		{"/alerts/rules/" + "11111111-1111-1111-1111-111111111111", "alerts:rules:read", []string{"alerts:alerts:read"}},
		// Include the OLD wrong spelling alongside the unrelated cap so a
		// regression that reverts to `alerts:maintenance-windows:read`
		// trips this denial test, not just the catalog-only mutations
		// table below.
		{"/alerts/maintenance-windows", "maintenance:windows:read", []string{"alerts:rules:read", "alerts:maintenance-windows:read"}},
		{"/alerts/maintenance-windows/11111111-1111-1111-1111-111111111111", "maintenance:windows:read", []string{"alerts:rules:read", "alerts:maintenance-windows:read"}},
	}
	for _, tc := range cases {
		t.Run(tc.path+"_AllowedWithCap", func(t *testing.T) {
			r := chi.NewRouter()
			(&Handler{Q: &fakeQ{}}).Mount(r)
			req := authtest.Request(http.MethodGet, tc.path, authtest.PrincipalWithCaps(tc.cap), nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code == http.StatusForbidden {
				t.Fatalf("with %s: got 403 (cap should permit)", tc.cap)
			}
		})
		t.Run(tc.path+"_DeniedWithOtherCap", func(t *testing.T) {
			r := chi.NewRouter()
			(&Handler{Q: &fakeQ{}}).Mount(r)
			req := authtest.Request(http.MethodGet, tc.path, authtest.PrincipalWithCaps(tc.others...), nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("with %v (no matching read cap): expected 403, got %d", tc.others, rec.Code)
			}
		})
	}
}

// Mutation cap codes match the canonical catalog
// (internal/admin/capabilities.go): alerts:alerts:ack (NOT :update)
// for ack; maintenance:windows:* (NOT alerts:maintenance-windows:*)
// for MW writes. Hold the wrong cap → 403.
func TestAlerts_MutationCapCodesMatchCatalog(t *testing.T) {
	cases := []struct {
		method, path string
		correctCap   string
		wrongCap     string
	}{
		{http.MethodPost, "/alerts/11111111-1111-1111-1111-111111111111/ack", "alerts:alerts:ack", "alerts:alerts:update"},
		{http.MethodPost, "/alerts/maintenance-windows", "maintenance:windows:create", "alerts:maintenance-windows:create"},
		{http.MethodPatch, "/alerts/maintenance-windows/11111111-1111-1111-1111-111111111111", "maintenance:windows:update", "alerts:maintenance-windows:update"},
		{http.MethodDelete, "/alerts/maintenance-windows/11111111-1111-1111-1111-111111111111", "maintenance:windows:delete", "alerts:maintenance-windows:delete"},
	}
	for _, tc := range cases {
		t.Run(tc.method+"_"+tc.path+"_WrongCap_403", func(t *testing.T) {
			r := chi.NewRouter()
			(&Handler{Q: &fakeQ{}}).Mount(r)
			req := authtest.Request(tc.method, tc.path, authtest.PrincipalWithCaps(tc.wrongCap), nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("with wrong cap %q: expected 403, got %d body=%s",
					tc.wrongCap, rec.Code, rec.Body.String())
			}
		})
	}
}
