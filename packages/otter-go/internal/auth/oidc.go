// OIDC code flow: discovery, login URL building, callback exchange +
// id_token verification, user upsert, session JWT minting.
//
// Mirrors packages/otter/src/dcim/api/auth.py:
//   /oidc/login    → 302 to issuer's authorization_endpoint
//   /oidc/callback → exchange code for tokens, verify id_token (incl.
//                    nonce), upsert user, mint and return session JWT
//   /oidc/logout   → 302 to issuer's end_session_endpoint
//
// Refresh-token persistence and the /auth/refresh endpoint are deferred
// to PR 37 — that flow needs the Fernet-equivalent encrypt path that
// isn't worth standing up just for this PR.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCConfig is the subset of OIDC settings the Go side needs.
// PublicURL rewrites the issuer's authorization_endpoint to a host
// the browser can reach when the IdP's internal hostname (e.g.
// host.docker.internal) isn't browser-resolvable.
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURI  string
	PublicURL    string
	// MFAAMRValues lists which amr claim entries (RFC 8176) count as
	// satisfying the deployment's MFA policy. "mfa", "otp", "hwk", ...
	MFAAMRValues []string
}

// OIDC bundles the discovery doc, verifier, and oauth2 client built
// once at startup. The provider lazily fetches JWKS and caches them.
type OIDC struct {
	provider  *oidc.Provider
	verifier  *oidc.IDTokenVerifier
	oauth2    *oauth2.Config
	cfg       OIDCConfig
	publicURL *url.URL
}

// NewOIDC builds the provider and verifier. Returns nil + nil when
// OIDC isn't configured (issuer or client_id empty); callers should
// treat that as "skip the oidc handlers" rather than an error.
func NewOIDC(ctx context.Context, cfg OIDCConfig) (*OIDC, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" {
		return nil, nil
	}
	p, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	o := &OIDC{
		provider: p,
		verifier: p.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		oauth2: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     p.Endpoint(),
			RedirectURL:  cfg.RedirectURI,
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
		cfg: cfg,
	}
	if cfg.PublicURL != "" {
		u, err := url.Parse(cfg.PublicURL)
		if err != nil {
			return nil, fmt.Errorf("oidc_public_url: %w", err)
		}
		o.publicURL = u
	}
	return o, nil
}

// LoginURL returns the URL the browser should be 302'd to. callback
// overrides the configured redirect URI for the rare case a SPA wants
// a per-launch callback (must still be allowlisted at the IdP).
func (o *OIDC) LoginURL(state, nonce, callback string) string {
	cfg := *o.oauth2
	if callback != "" {
		cfg.RedirectURL = callback
	}
	opts := []oauth2.AuthCodeOption{}
	if nonce != "" {
		opts = append(opts, oidc.Nonce(nonce))
	}
	u := cfg.AuthCodeURL(state, opts...)
	if o.publicURL == nil {
		return u
	}
	// Swap scheme+host on the authorization endpoint so the browser
	// hits the IdP at its externally-reachable name, not whatever the
	// discovery doc returned for internal use.
	parsed, err := url.Parse(u)
	if err != nil {
		return u
	}
	parsed.Scheme = o.publicURL.Scheme
	parsed.Host = o.publicURL.Host
	return parsed.String()
}

// EndSessionURL returns the IdP's end_session_endpoint URL with the
// caller-supplied id_token_hint and post_logout_redirect_uri appended.
// Returns "" if the IdP didn't advertise end_session_endpoint.
func (o *OIDC) EndSessionURL(idTokenHint, postLogoutRedirectURI string) string {
	var claims struct {
		EndSessionEndpoint string `json:"end_session_endpoint"`
	}
	if err := o.provider.Claims(&claims); err != nil || claims.EndSessionEndpoint == "" {
		return ""
	}
	q := url.Values{}
	if idTokenHint != "" {
		q.Set("id_token_hint", idTokenHint)
	}
	if postLogoutRedirectURI != "" {
		q.Set("post_logout_redirect_uri", postLogoutRedirectURI)
	}
	sep := "?"
	if strings.Contains(claims.EndSessionEndpoint, "?") {
		sep = "&"
	}
	if len(q) == 0 {
		return claims.EndSessionEndpoint
	}
	return claims.EndSessionEndpoint + sep + q.Encode()
}

// CallbackResult is what the /oidc/callback handler hands back to the
// caller for JWT minting + user upsert.
type CallbackResult struct {
	Subject  string
	Email    string
	Name     string
	IDToken  string
	IdpRoles []string
	MFA      bool
}

// HandleCode performs the code→tokens→id_token-verify chain. nonce
// must match the value the SPA stashed at /login time; failing the
// check returns an error. callback overrides the configured redirect
// URI to match the value used at /login.
func (o *OIDC) HandleCode(ctx context.Context, code, nonce, callback string) (*CallbackResult, error) {
	if nonce == "" {
		return nil, errors.New("oidc nonce required")
	}
	cfg := *o.oauth2
	if callback != "" {
		cfg.RedirectURL = callback
	}
	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("oidc token exchange: %w", err)
	}
	rawIDToken, _ := tok.Extra("id_token").(string)
	if rawIDToken == "" {
		return nil, errors.New("oidc response missing id_token")
	}
	idTok, err := o.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("oidc id_token invalid: %w", err)
	}
	if idTok.Nonce != nonce {
		return nil, errors.New("oidc id_token nonce mismatch")
	}
	var claims map[string]any
	if err := idTok.Claims(&claims); err != nil {
		return nil, fmt.Errorf("oidc id_token claims: %w", err)
	}
	sub, _ := claims["sub"].(string)
	email, _ := claims["email"].(string)
	if email == "" {
		email, _ = claims["preferred_username"].(string)
	}
	if sub == "" || email == "" {
		return nil, errors.New("oidc claims missing sub/email")
	}
	name, _ := claims["name"].(string)
	return &CallbackResult{
		Subject:  sub,
		Email:    email,
		Name:     name,
		IDToken:  rawIDToken,
		IdpRoles: extractIdpRoles(claims),
		MFA:      mfaSatisfied(claims, o.cfg.MFAAMRValues),
	}, nil
}

// extractIdpRoles mirrors Python _extract_idp_roles. Covers Keycloak
// (realm_access.roles + resource_access.<client>.roles), Okta/ADFS
// (groups, roles). Returns deduped sorted slice for stable JWT shape.
func extractIdpRoles(claims map[string]any) []string {
	seen := map[string]struct{}{}
	add := func(v any) {
		if arr, ok := v.([]any); ok {
			for _, item := range arr {
				if s, ok := item.(string); ok && s != "" {
					seen[s] = struct{}{}
				}
			}
		}
	}
	if ra, ok := claims["realm_access"].(map[string]any); ok {
		add(ra["roles"])
	}
	if rsa, ok := claims["resource_access"].(map[string]any); ok {
		for _, v := range rsa {
			if block, ok := v.(map[string]any); ok {
				add(block["roles"])
			}
		}
	}
	add(claims["groups"])
	add(claims["roles"])
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	// Sort for stable JWT payloads (helps caching + test stability).
	sortStrings(out)
	return out
}

func mfaSatisfied(claims map[string]any, mfaValues []string) bool {
	if len(mfaValues) == 0 {
		return false
	}
	amr, ok := claims["amr"].([]any)
	if !ok {
		return false
	}
	for _, item := range amr {
		s, ok := item.(string)
		if !ok {
			continue
		}
		for _, v := range mfaValues {
			if s == v {
				return true
			}
		}
	}
	return false
}

// Small helper to avoid pulling in sort just for one call site that
// runs once per login. Bubble sort is fine for tiny lists; this gets
// hit only on /oidc/callback.
func sortStrings(s []string) {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}
