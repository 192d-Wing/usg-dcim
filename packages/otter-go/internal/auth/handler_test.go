package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// These tests cover the OIDC handler paths that don't require a real
// IdP. End-to-end OIDC integration testing would need a Keycloak in
// CI; deferred.

func mountAuth(h *Handler) http.Handler {
	r := chi.NewRouter()
	h.Mount(r)
	return r
}

func TestOIDCLogin_400WhenUnconfigured(t *testing.T) {
	h := &Handler{Q: &fakeQ{}, OIDC: nil}
	rec := httptest.NewRecorder()
	mountAuth(h).ServeHTTP(rec, httptest.NewRequest("GET", "/auth/oidc/login", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d", rec.Code)
	}
}

func TestOIDCCallback_400WhenUnconfigured(t *testing.T) {
	h := &Handler{Q: &fakeQ{}, OIDC: nil}
	rec := httptest.NewRecorder()
	mountAuth(h).ServeHTTP(rec, httptest.NewRequest("GET", "/auth/oidc/callback?code=x", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d", rec.Code)
	}
}

func TestOIDCLogout_302FallbackWhenUnconfigured(t *testing.T) {
	h := &Handler{Q: &fakeQ{}, OIDC: nil}
	rec := httptest.NewRecorder()
	mountAuth(h).ServeHTTP(rec, httptest.NewRequest("GET", "/auth/oidc/logout?post_logout_redirect_uri=/bye", nil))
	if rec.Code != http.StatusFound {
		t.Errorf("got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/bye" {
		t.Errorf("Location: %q", loc)
	}
}

func TestExtractIdpRoles_Keycloak(t *testing.T) {
	claims := map[string]any{
		"realm_access": map[string]any{
			"roles": []any{"network-admin", "operator"},
		},
		"resource_access": map[string]any{
			"dcim-spa": map[string]any{
				"roles": []any{"viewer", "operator"}, // dup
			},
		},
		"groups": []any{"hq-staff"},
	}
	roles := extractIdpRoles(claims)
	want := map[string]bool{"network-admin": true, "operator": true, "viewer": true, "hq-staff": true}
	if len(roles) != len(want) {
		t.Fatalf("len: got %v", roles)
	}
	for _, r := range roles {
		if !want[r] {
			t.Errorf("unexpected role %q", r)
		}
	}
}

func TestMFASatisfied(t *testing.T) {
	cases := []struct {
		amr  []any
		vals []string
		want bool
	}{
		{[]any{"mfa", "pwd"}, []string{"mfa", "otp"}, true},
		{[]any{"pwd"}, []string{"mfa", "otp"}, false},
		{nil, []string{"mfa"}, false},
		{[]any{"otp"}, nil, false},
	}
	for _, c := range cases {
		got := mfaSatisfied(map[string]any{"amr": c.amr}, c.vals)
		if got != c.want {
			t.Errorf("amr=%v vals=%v: got %v want %v", c.amr, c.vals, got, c.want)
		}
	}
}
