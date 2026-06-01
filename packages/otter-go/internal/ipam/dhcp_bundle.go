// Go port of Python's GET /api/v1/dhcp/servers/{id}/bundle handler
// (api/ipam.py:1833). Returns a complete Kea config bundle for the
// dhcp-site chart's bundle-puller to install — the operator owns
// everything in ctrl_agent/dhcp4/dhcp6 except the subnet arrays
// (DCIM authors those from DhcpScope rows; see PR #216's
// internal/dhcp/bundle for the merge contract).
//
// Two paths:
//
//  1. Cache hit. When bundle_cache_etag + bundle_cache_json are both
//     populated (the rerender_dhcp_bundle scheduler job, followup PR,
//     writes them on every scope mutation), the handler returns the
//     cached JSON directly. An `If-None-Match` header carrying the
//     same etag short-circuits to 304.
//
//  2. Cache miss / fresh DB. Live render: list every live scope,
//     bulk-load any referenced templates, run the renderer. The
//     handler still honors `If-None-Match` against the freshly
//     computed etag so a client repeatedly polling without changes
//     pays the render cost once but no body bytes after.
//
// Capability is `ipam:dhcp-servers:bundle` — separate from `:read`
// so operators can grant the heavy collector-facing endpoint without
// exposing the rest of the DHCP surface.
package ipam

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/bundle"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

const errDhcpServerNotFound = "dhcp server not found"

func (h *Handler) getDhcpServerBundle(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	srv, err := h.Q.GetDhcpServerBundleRow(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, errDhcpServerNotFound)
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if !h.enforceFabric(w, r, srv.FabricID, "ipam:dhcp-servers:bundle") {
		return
	}
	ifNoneMatch := stripQuotes(r.Header.Get("If-None-Match"))

	// Cache hit short-circuit. Both columns must be populated — a
	// row mid-bootstrap (etag written but JSON still empty, or vice
	// versa) falls through to live render so the puller never
	// receives a half-baked bundle. The empty-string check on the
	// etag matches Python's `if server.bundle_cache_etag and ...`
	// truthy semantics — without it a *string pointing at "" would
	// satisfy the != nil branch and serve an empty-etag cache hit.
	if srv.BundleCacheEtag != nil && *srv.BundleCacheEtag != "" && len(srv.BundleCacheJSON) > 0 {
		if ifNoneMatch != "" && ifNoneMatch == *srv.BundleCacheEtag {
			// RFC 9110 §15.4.5 — a 304 response SHOULD include the
			// ETag so an intermediary/proxy can refresh its
			// validator without a second round-trip. Python's
			// `Response(status_code=304)` omits it; Go fills it in.
			w.Header().Set("ETag", `"`+*srv.BundleCacheEtag+`"`)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		// Mirror Python's `return server.bundle_cache_json` —
		// the cache stores the full bundle wire shape, so write
		// the raw JSON straight to the response without a
		// round-trip through map[string]any (which would scramble
		// the etag-canonical key ordering on the way back out and
		// produce a body that hashes differently from the etag
		// header the puller's seen).
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"`+*srv.BundleCacheEtag+`"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(srv.BundleCacheJSON)
		return
	}

	bundleOut, err := liveRenderBundle(r, h.Q, srv)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if ifNoneMatch != "" && ifNoneMatch == bundleOut.Etag {
		// Same RFC 9110 §15.4.5 ETag-on-304 rationale as above.
		w.Header().Set("ETag", `"`+bundleOut.Etag+`"`)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", `"`+bundleOut.Etag+`"`)
	// NOTE on body-vs-etag shape divergence (parity with Python):
	// bundleOut.Etag was computed over the canonical 3-key subset
	// {ctrl-agent, dhcp4, dhcp6} sorted alphabetically (see
	// bundle/bundle.go:computeEtag). The body we write here is the
	// full KeaBundle struct in declaration order — server_id +
	// ctrl_agent + dhcp4 + dhcp6 + etag (note `ctrl_agent` snake_case
	// in the body vs `ctrl-agent` kebab-case in the etag's canonical
	// form). Body-hash will NOT equal the etag; clients (the
	// dhcp-site puller is the only known one) must trust the ETag
	// header rather than re-hash the body. PR 4's cache writer
	// should serialize the same struct via httpx.JSON-equivalent so
	// the cache-hit path and live-render path produce byte-identical
	// bodies for the same logical bundle.
	httpx.JSON(w, http.StatusOK, bundleOut)
}

// liveRenderBundle pulls scopes + templates and runs the renderer
// via the shared bundle.BuildForServer helper — same call shape
// the rerender cron uses, so there's exactly one place that
// orchestrates the read-and-render dance.
func liveRenderBundle(r *http.Request, q Querier, srv dbq.DhcpServerBundleRow) (bundle.KeaBundle, error) {
	return bundle.BuildForServer(r.Context(), q, srv)
}

// stripQuotes drops the surrounding double-quotes If-None-Match
// carries per RFC 9110 §8.8.3 (the etag header form). A bare etag
// (no quotes) is accepted too — some HTTP clients omit them on the
// way in even though the spec mandates them on the way out.
func stripQuotes(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}
