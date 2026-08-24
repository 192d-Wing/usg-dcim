package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func getMethods(t *testing.T, h *Handler) (int, methodsResponse, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	// No principal injected — /auth/methods must answer unauthenticated
	// (it renders the login screen).
	mountAuth(h).ServeHTTP(rec, httptest.NewRequest("GET", "/auth/methods", nil))
	var resp methodsResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("bad JSON: %v (%s)", err, rec.Body.String())
		}
	}
	return rec.Code, resp, rec.Body.String()
}

func TestMethods_NoOIDC(t *testing.T) {
	code, resp, body := getMethods(t, &Handler{Q: &fakeQ{}, OIDC: nil})
	if code != http.StatusOK {
		t.Fatalf("got %d", code)
	}
	if !resp.Local {
		t.Error("local must be true")
	}
	if resp.SSO {
		t.Error("sso must be false when OIDC is unconfigured")
	}
	// omitempty: no dangling login URL when there is nothing to log
	// into — finch treats its absence as "no SSO".
	if strings.Contains(body, "sso_login_url") {
		t.Errorf("sso_login_url should be omitted, got %s", body)
	}
}

func TestMethods_WithOIDC(t *testing.T) {
	code, resp, _ := getMethods(t, &Handler{Q: &fakeQ{}, OIDC: &OIDC{}})
	if code != http.StatusOK {
		t.Fatalf("got %d", code)
	}
	if !resp.Local || !resp.SSO {
		t.Errorf("want local+sso true, got %+v", resp)
	}
	// Derived from the request path (mounted at /auth here; /api/v1/auth
	// in production) so the mount prefix is preserved automatically.
	if resp.SSOLoginURL != "/auth/oidc/login" {
		t.Errorf("sso_login_url = %q, want /auth/oidc/login", resp.SSOLoginURL)
	}
}
