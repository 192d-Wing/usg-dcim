package auth

import (
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"

	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// Handler mounts the read + OIDC auth endpoints under /auth/. Local
// /auth/login, /auth/refresh, /auth/logout (jti revocation), and the
// /auth/tokens CRUD endpoints ship in PR 37.
type Handler struct {
	Q      Querier
	OIDC   *OIDC         // nil when DCIM_OIDC_ISSUER unset; the OIDC handlers 400 in that case
	Mint   MintConfig    // session-JWT signing config; matches the verifier's PrimarySecret
	Fernet FernetConfig  // refresh-token at-rest encryption; empty Keys → plaintext fallback
	Audit  AuditRecorder // nil-safe; when set, login/logout/token-CRUD/refresh write audit rows
}

func (h *Handler) Mount(r chi.Router) {
	r.Route("/auth", func(r chi.Router) {
		// Authentication-only — Verifying middleware upstream of /api/v1
		// has already validated the bearer.
		r.Get("/me", h.me)
		r.Get("/oidc/login", h.oidcLogin)
		r.Get("/oidc/callback", h.oidcCallback)
		r.Get("/oidc/logout", h.oidcLogout)
		r.Post("/login", h.login)
		r.Post("/logout", h.logout)
		r.Post("/refresh", h.refresh)
		// /tokens gates are doubly enforced. The middleware wrap is the
		// grep-able route-layer convention (matches admin, audit,
		// telemetry, lir, alerts, …) and the canonical 403 path. The
		// inline HasCapability checks in handler_local.go remain as
		// belt-and-suspenders so a future refactor that removes either
		// layer still fails closed. issueToken additionally walks the
		// requested permission_codes inline for no-escalation — that
		// data check can't move to middleware.
		r.With(RequireCapability("admin:api-tokens:read")).Get("/tokens", h.listTokens)
		r.With(RequireCapability("admin:api-tokens:create")).Post("/tokens", h.issueToken)
		r.With(RequireCapability("admin:api-tokens:delete")).Delete("/tokens/{id}", h.revokeToken)
	})
}

type meUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

type meResponse struct {
	User         *meUser  `json:"user"`
	ViaToken     bool     `json:"via_token"`
	Capabilities []string `json:"capabilities"`
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	p, ok := From(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "no principal")
		return
	}
	caps := append([]string(nil), p.Capabilities...)
	sort.Strings(caps)
	resp := meResponse{ViaToken: false, Capabilities: caps}
	if u, err := h.Q.GetUser(r.Context(), p.Subject); err == nil {
		resp.User = &meUser{ID: u.ID.String(), Email: u.Email}
	} else {
		resp.User = &meUser{ID: p.Subject.String()}
	}
	httpx.JSON(w, http.StatusOK, resp)
}

// oidcLogin redirects the browser to the IdP. state + nonce are
// forwarded verbatim; the SPA mints them before the navigation and
// re-validates on callback (CSRF and id_token-substitution defenses).
func (h *Handler) oidcLogin(w http.ResponseWriter, r *http.Request) {
	if h.OIDC == nil {
		httpx.Error(w, http.StatusBadRequest, "OIDC not configured")
		return
	}
	q := r.URL.Query()
	url := h.OIDC.LoginURL(q.Get("state"), q.Get("nonce"), q.Get("redirect_uri"))
	http.Redirect(w, r, url, http.StatusFound)
}

// tokenOut mirrors Python TokenOut so the SPA's existing parser keeps
// working unchanged. id_token is echoed back for RP-initiated logout
// later via /oidc/logout's id_token_hint.
type tokenOut struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	IDToken     string `json:"id_token,omitempty"`
}

// oidcCallback runs the code-exchange chain, upserts the user, and
// returns a freshly-minted session JWT. The SPA stashes both the
// access_token (for our APIs) and the id_token (for IdP logout).
func (h *Handler) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if h.OIDC == nil {
		httpx.Error(w, http.StatusBadRequest, "OIDC not configured")
		return
	}
	q := r.URL.Query()
	code := q.Get("code")
	if code == "" {
		httpx.Error(w, http.StatusBadRequest, "code required")
		return
	}
	res, err := h.OIDC.HandleCode(r.Context(), code, q.Get("nonce"), q.Get("redirect_uri"))
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	user, err := upsertOidcUser(r.Context(), h.Q, res.Subject, res.Email, res.Name)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "user upsert failed")
		return
	}
	tok, _, err := Mint(h.Mint, user.ID, res.IdpRoles, res.MFA)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "jwt mint failed")
		return
	}
	uid := user.ID
	h.auditAuth(r.Context(), "auth.oidc_login", user.ID.String(), &uid, "user:"+user.ID.String(), true,
		map[string]any{"method": "oidc", "sub": res.Subject, "email": res.Email, "mfa": res.MFA})
	httpx.JSON(w, http.StatusOK, tokenOut{
		AccessToken: tok,
		ExpiresIn:   h.Mint.TTLSecond,
		IDToken:     res.IDToken,
	})
}

// oidcLogout 302s to the IdP's end_session_endpoint. Falls back to
// post_logout_redirect_uri (or /login) when OIDC isn't configured or
// the IdP didn't advertise end_session_endpoint.
func (h *Handler) oidcLogout(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fallback := q.Get("post_logout_redirect_uri")
	if fallback == "" {
		fallback = "/login"
	}
	if h.OIDC == nil {
		http.Redirect(w, r, fallback, http.StatusFound)
		return
	}
	url := h.OIDC.EndSessionURL(q.Get("id_token_hint"), q.Get("post_logout_redirect_uri"))
	if url == "" {
		http.Redirect(w, r, fallback, http.StatusFound)
		return
	}
	http.Redirect(w, r, url, http.StatusFound)
}
