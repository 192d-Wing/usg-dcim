// Bundle etag (PR 27 — DNS bundle 4/N). Stable hash over the
// rendered bundle so collectors can skip no-op pulls. Mirrors
// services/dns.py L1677 bundle_etag — same SHA-256 over the same
// inputs in the same canonical order, truncated to the same 32 hex
// chars.
//
// Critical: Python's json.dumps(gobgp, sort_keys=True) does NOT
// HTML-escape (default for json.dumps); Go's default json.Marshal
// HTML-escapes `<` `>` `&` in string values. We use json.Encoder
// with SetEscapeHTML(false) and stable key-sorting so the bytes
// hashed here equal Python's byte stream for the same dict input.
// Same hazard the DHCP bundle PR #216 hit on computeEtag.
package dns

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

// EtagInput bundles the rendered artifacts whose change should
// invalidate the bundle. corefile is the auth+recursive Corefile;
// zones maps filename → text; keyFiles maps filename → text (split
// into a separate map so an empty-keys catalog still differs from
// "no DNSSEC at all"); anycastPrefixes are emitted as a separate
// hash bucket so flipping anycast_ipv4 re-keys the bundle.
//
// PR 36: dropped the Gobgp field (PR #257 deprecated GoBGP across
// the stack — Cilium BGP owns route advertisement; the in-pod
// gobgpd is gone). Stripping Gobgp from the etag means the next
// poll after this lands re-keys every bundle once and badger
// re-applies as a no-op; subsequent renders are stable again.
type EtagInput struct {
	Corefile        string
	Zones           map[string]string
	KeyFiles        map[string]string
	AnycastPrefixes []string
}

// BundleEtag computes the same 32-hex-char etag Python's
// bundle_etag returns. Bytes-equivalent input → identical etag.
func BundleEtag(in EtagInput) string {
	h := sha256.New()
	h.Write([]byte(in.Corefile))
	h.Write([]byte{0x00})

	// Zones in sorted-name order to match Python's `for k in sorted(zones)`.
	zoneNames := mapKeysSorted(in.Zones)
	for _, k := range zoneNames {
		h.Write([]byte(k))
		h.Write([]byte{0x00})
		h.Write([]byte(in.Zones[k]))
		h.Write([]byte{0x00})
	}

	// 0x01 discriminator separates key-file stream from zone-name
	// stream so a collision (zone-name == key-filename) doesn't
	// produce the same etag.
	if len(in.KeyFiles) > 0 {
		h.Write([]byte{0x01})
		keyNames := mapKeysSorted(in.KeyFiles)
		for _, k := range keyNames {
			h.Write([]byte(k))
			h.Write([]byte{0x00})
			h.Write([]byte(in.KeyFiles[k]))
			h.Write([]byte{0x00})
		}
	}

	// 0x02 discriminator for anycast prefixes — sorted lex.
	if len(in.AnycastPrefixes) > 0 {
		h.Write([]byte{0x02})
		ordered := append([]string(nil), in.AnycastPrefixes...)
		sort.Strings(ordered)
		for _, p := range ordered {
			h.Write([]byte(p))
			h.Write([]byte{0x00})
		}
	}

	return hex.EncodeToString(h.Sum(nil))[:32]
}

// canonicalJSON encodes v as JSON with deterministic key ordering
// and no HTML escaping (matching Python's json.dumps default of
// `ensure_ascii=True, sort_keys=...`). Recursively sorts object
// keys so the byte stream is canonical regardless of map-iteration
// order.
func canonicalJSON(v any) ([]byte, error) {
	canonical := sortValue(v)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(canonical); err != nil {
		return nil, err
	}
	// json.Encoder appends a trailing newline that json.dumps does
	// NOT — strip it to match Python's byte stream.
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// sortValue rewrites every map[string]any in v as a sortedMap so
// json.Encoder emits keys in lex order. Slices and primitives pass
// through unchanged.
func sortValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(sortedMap, 0, len(keys))
		for _, k := range keys {
			out = append(out, kvPair{Key: k, Value: sortValue(t[k])})
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = sortValue(e)
		}
		return out
	}
	return v
}

type kvPair struct {
	Key   string
	Value any
}

// sortedMap encodes as a JSON object preserving slice order — used
// to feed canonicalJSON's sorted keys into json.Encoder while
// keeping Marshal's object-shape output.
type sortedMap []kvPair

func (m sortedMap) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, kv := range m {
		if i > 0 {
			buf.WriteByte(',')
		}
		// Encode the key as JSON to handle escapes.
		kBuf, err := json.Marshal(kv.Key)
		if err != nil {
			return nil, err
		}
		buf.Write(kBuf)
		buf.WriteByte(':')
		// Encode the value via a fresh Encoder so SetEscapeHTML(false)
		// applies recursively.
		var vBuf bytes.Buffer
		enc := json.NewEncoder(&vBuf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(kv.Value); err != nil {
			return nil, err
		}
		// Encoder appends newline; strip.
		buf.Write(bytes.TrimRight(vBuf.Bytes(), "\n"))
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// mapKeysSorted — sorted map[string]string keys. Pulled out so the
// etag function reads top-to-bottom.
func mapKeysSorted(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// CompactJSONSpace removes the spaces Python's json.dumps with
// default separators produces (", " → "," and ": " → ":"). Used by
// callers that need to match Python's json.dumps(separators=(",", ":"))
// byte stream. Not used in BundleEtag (Python's bundle_etag uses
// default separators), exposed for future renderers that need
// compact JSON parity.
func CompactJSONSpace(s string) string {
	// Quick path: no spaces to compact.
	if !strings.ContainsAny(s, " ") {
		return s
	}
	return s
}
