// GET /auth/methods — tells the login screen which sign-in paths this
// deployment actually supports, so the SPA renders from runtime truth
// instead of a frontend build-time constant. Motivating bug: clusters
// without OIDC (e.g. the windep dev cluster) showed a dead E-ICAM
// button because finch's loginBranding.sso.enabled guessed wrong.
package auth

import (
	"net/http"
	"strings"

	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// methodsResponse is the wire shape finch's login page consumes.
type methodsResponse struct {
	// Local is whether email/password login is available. Always true
	// today — /auth/login is unconditionally mounted — but explicit in
	// the contract so an SSO-only deployment can turn it off later
	// without a shape change.
	Local bool `json:"local"`
	// SSO is whether the OIDC flow is configured and the E-ICAM button
	// should render.
	SSO bool `json:"sso"`
	// SSOLoginURL is the navigation target for the SSO button — the
	// endpoint that 302s to the IdP. Present only when SSO is true.
	SSOLoginURL string `json:"sso_login_url,omitempty"`
}

// methods reports the available auth methods. Public by design: the
// login screen calls it before any session exists, so it mounts in
// publicRoutes. SSO is "on" exactly when the OIDC provider was built
// at startup — auth.NewOIDC returns nil when DCIM_OIDC_ISSUER /
// client_id are unset — which is the same nil-check the /oidc/*
// handlers use for their "OIDC not configured" 400s, so the flag can
// never disagree with the flow it advertises.
func (h *Handler) methods(w http.ResponseWriter, r *http.Request) {
	resp := methodsResponse{Local: true, SSO: h.OIDC != nil}
	if resp.SSO {
		// Derive the login URL from this request's own path rather
		// than hard-coding the /api/v1 mount prefix: /auth/methods and
		// /auth/oidc/login are registered on the same router, so
		// whatever prefix reached us also reaches the login route.
		resp.SSOLoginURL = strings.TrimSuffix(r.URL.Path, "/methods") + "/oidc/login"
	}
	httpx.JSON(w, http.StatusOK, resp)
}
