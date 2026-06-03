// HTTP handler for GET /dns/servers/{id}/bundle (PR 30 — DNS bundle
// 7/N). Loads the bundle inputs from the database and feeds them
// into AssembleAuthBundle, then writes the response with the etag
// header for cache-skip semantics.
//
// Wire-shape mirror of Python's server_bundle endpoint at
// api/dns.py L1095. Both 200 and 304 set the ETag header so the
// collector can do conditional fetches via If-None-Match.
//
// Coverage gaps vs Python (documented for the follow-up cutover):
//   - Catalog zones: caller-supplied empty here; the catalog fetch
//     + auth-server primaries collection ports in a follow-up.
//   - DS-record + CDNSKEY/CDS extras lines: empty until the parent-
//     zone-walk helper ports.
//   - Split-horizon views: view-bound records flow into the default
//     zone file (Python would have split them). Cutover blocks
//     until view rendering ports.
//   - Recursive servers: not handled here; that path needs the
//     GoBGP YAML renderer + RPZ zone renderer + recursive engine
//     selection helpers, all deferred to follow-up PRs.
package dns

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// bundleQuerier is the slice of Querier methods the bundle endpoint
// needs. Declared narrow so the test fake doesn't have to implement
// the full Querier surface.
type bundleQuerier interface {
	GetDnsServer(ctx context.Context, id uuid.UUID) (dbq.DnsServer, error)
	ListDnsZonesByFabric(ctx context.Context, fabricID uuid.UUID) ([]dbq.DnsZone, error)
	ListDnsRecordsByZoneIDs(ctx context.Context, zoneIDs []uuid.UUID) ([]dbq.DnsRecordForBundle, error)
	ListUnhealthyEnabledHealthChecksByFabric(ctx context.Context, fabricID uuid.UUID) ([]uuid.UUID, error)
}

// loadAuthBundleInput fetches the data an auth-server bundle needs.
// Pure orchestration: no rendering. Returns the AuthBundleInput
// ready to feed AssembleAuthBundle.
func loadAuthBundleInput(ctx context.Context, q bundleQuerier, server dbq.DnsServer) (AuthBundleInput, error) {
	in := AuthBundleInput{Server: server}

	zones, err := q.ListDnsZonesByFabric(ctx, server.FabricID)
	if err != nil {
		return in, err
	}
	in.Zones = zones

	zoneIDs := make([]uuid.UUID, len(zones))
	for i, z := range zones {
		zoneIDs[i] = z.ID
	}
	records, err := q.ListDnsRecordsByZoneIDs(ctx, zoneIDs)
	if err != nil {
		return in, err
	}
	byZone := make(map[uuid.UUID][]dbq.DnsRecordForBundle, len(zones))
	for _, r := range records {
		byZone[r.ZoneID] = append(byZone[r.ZoneID], r)
	}
	in.RecordsByZone = byZone

	unhealthyIDs, err := q.ListUnhealthyEnabledHealthChecksByFabric(ctx, server.FabricID)
	if err != nil {
		return in, err
	}
	unhealthy := make(map[uuid.UUID]struct{}, len(unhealthyIDs))
	for _, id := range unhealthyIDs {
		unhealthy[id] = struct{}{}
	}
	in.UnhealthyCheckIDs = unhealthy

	// Maps caller-empty until the follow-up PR ports the rest of the
	// helpers. AssembleAuthBundle treats nil maps as "no DNSSEC, no
	// catalog, no extras" — degrades cleanly.
	in.KeyFiles = map[string]string{}
	return in, nil
}

// getDnsServerBundle is the HTTP handler. Loads the bundle, computes
// etag, writes 200 (or 304 on If-None-Match match).
//
// NOTE: this handler is intentionally NOT mounted in handler.go yet.
// The cutover lands in a follow-up PR once the catalog/DNSSEC/extras/
// split-horizon helpers are in place. Mounting prematurely would
// silently serve incomplete bundles to collectors.
func (h *Handler) getDnsServerBundle(w http.ResponseWriter, r *http.Request) {
	h.bundleHandlerWith(w, r, h.Q)
}

// bundleHandlerWith is the implementation, parameterized on the
// querier so tests can inject a narrow fake without going through
// the full DNS Querier surface.
func (h *Handler) bundleHandlerWith(w http.ResponseWriter, r *http.Request, q bundleQuerier) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	server, err := q.GetDnsServer(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "dns server not found")
			return
		}
		mapErr(w, err, "dns server not found")
		return
	}
	// Recursive servers need GoBGP + RPZ + recursive-engine helpers
	// not yet ported. Reject loudly so a misrouted client doesn't get
	// a degraded auth bundle that doesn't apply.
	if server.Role != "auth" {
		httpx.Error(w, http.StatusNotImplemented, "recursive bundle not yet ported")
		return
	}
	in, err := loadAuthBundleInput(r.Context(), q, server)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	bundle, err := AssembleAuthBundle(in)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	// Conditional fetch. The collector caches the etag and sends
	// If-None-Match on every poll; a match returns 304 with no
	// body so we save the JSON serialization + network round-trip.
	w.Header().Set("ETag", `"`+bundle.Etag+`"`)
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		if etagMatches(inm, bundle.Etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	httpx.JSON(w, http.StatusOK, bundle)
}

// etagMatches accepts both the quoted form (`"abcd"`) and the bare
// form (`abcd`). RFC 9110 says clients SHOULD quote but DCIM's
// curl-driven smoke tests don't, and Python's handler also accepts
// either form.
func etagMatches(headerVal, etag string) bool {
	stripped := headerVal
	if len(stripped) >= 2 && stripped[0] == '"' && stripped[len(stripped)-1] == '"' {
		stripped = stripped[1 : len(stripped)-1]
	}
	return stripped == etag
}

// bundleHashCheck — sanity helper for tests + a future warm-cache
// integration: SHA-256 prefix over the serialized bundle. Distinct
// from the bundle's own etag (which is computed over the inputs);
// useful when verifying that two assemblers produce byte-identical
// JSON.
func bundleHashCheck(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:8])
}
