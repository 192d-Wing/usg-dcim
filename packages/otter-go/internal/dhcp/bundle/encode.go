// Cache-side serialization for the bundle. The dhcp_servers
// bundle_cache_json column is JSONB, which means Postgres normalizes
// the stored bytes — keys are re-sorted (shortest-first then lex),
// whitespace is stripped, duplicate keys are de-duped, and any
// trailing newline is dropped. Round-tripping bytes through a JSONB
// column therefore CANNOT preserve byte equality with the
// httpx.JSON output that the live-render path emits.
//
// PR #218's handler-side contract (see ipam/dhcp_bundle.go's
// "body-vs-etag shape divergence" comment) already established that
// the ETag header is the authoritative bundle identifier; consumers
// must NOT hash the response body for equality checks. This file
// extends that contract from the live-render path to the cache-hit
// path: both deliver valid JSON for the same logical bundle, but the
// exact bytes will differ — JSONB normalization on cache-hit,
// encoder defaults on live-render.
//
// We therefore use the cheapest valid-JSON encoder for the cache
// write: json.Marshal. No trailing newline, no special encoder
// settings — anything that produces valid JSON of the KeaBundle
// struct will round-trip through JSONB to equivalent semantic shape.
package bundle

import "encoding/json"

// EncodeForCache marshals a KeaBundle for storage in the JSONB
// bundle_cache_json column. The returned bytes are valid JSON that
// will round-trip through Postgres's JSONB normalization without
// losing information; they will NOT byte-equal what httpx.JSON
// emits on the live-render path for the same logical bundle (see
// package doc). Consumers must use the ETag header as the bundle
// identifier rather than hash the body.
func EncodeForCache(b KeaBundle) ([]byte, error) {
	return json.Marshal(b)
}
