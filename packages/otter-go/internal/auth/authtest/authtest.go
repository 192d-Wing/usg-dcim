// Package authtest carries the request/response helpers test files
// across otter-go's handler packages use to drive their HTTP entry
// points with a known Principal. Pulling these out of audit/telemetry/
// admin/lir/alerts/sites test files into one place means a single
// shared shape so a future cap-gate retrofit doesn't copy the same
// boilerplate again, and so the cap-check semantics (truthy/falsy
// principal, header-vs-context injection, etc.) live in one
// canonical spot.
//
// Test-only — handlers should not import this package.
package authtest

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

// PrincipalWithCaps returns a Principal carrying the given cap codes
// (no scope info — every cap is treated as Global) and a tag in
// Label so audit assertions can recognize it.
func PrincipalWithCaps(caps ...string) auth.Principal {
	return auth.Principal{
		Capabilities: caps,
		Label:        "authtest",
	}
}

// PrincipalWithScopes is the rarer constructor used by ABAC tests
// that need to set Principal.Scopes alongside Capabilities.
func PrincipalWithScopes(caps []string, scopes map[string]auth.Scope) auth.Principal {
	return auth.Principal{
		Capabilities: caps,
		Scopes:       scopes,
		Label:        "authtest",
	}
}

// Request builds an *http.Request with the given Principal injected
// into its context. The cache used by ScopedSiteFilter (added in
// PR 177 to memoize ABAC expansions per request) is also attached so
// handler tests exercise the same context shape the Verifying
// middleware sets up in production.
func Request(method, target string, p auth.Principal, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, target, body)
	ctx := auth.WithPrincipal(r.Context(), p)
	ctx = auth.WithScopeFilterCache(ctx)
	return r.WithContext(ctx)
}

// ServeRequest is the common test verb: build the principal-injected
// request, dispatch it against the handler, return the recorder so
// the test can assert on Code and Body. Equivalent to the doWith()
// helper that several handler test files re-create inline.
func ServeRequest(h http.Handler, p auth.Principal, method, target string, body io.Reader) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, Request(method, target, p, body))
	return rec
}

// NewContext returns a context with the principal AND scope-filter
// cache attached. Useful when a test calls into a non-HTTP helper
// (e.g. ScopedSiteFilter directly) and still wants the same setup
// the real middleware provides.
func NewContext(p auth.Principal) context.Context {
	ctx := auth.WithPrincipal(context.Background(), p)
	return auth.WithScopeFilterCache(ctx)
}
