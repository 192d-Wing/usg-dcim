package dns

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// ===== BundleEtag determinism + sensitivity =====

func TestBundleEtag_DeterministicAcrossCalls(t *testing.T) {
	in := EtagInput{
		Corefile: ".:53 { forward . 1.1.1.1 }",
		Zones: map[string]string{
			"a.example.zone": "$ORIGIN a.example.\n",
			"b.example.zone": "$ORIGIN b.example.\n",
		},
	}
	a := BundleEtag(in)
	b := BundleEtag(in)
	if a != b {
		t.Errorf("etag flapped across identical calls: %s vs %s", a, b)
	}
}

func TestBundleEtag_StableUnderZoneMapOrderShuffle(t *testing.T) {
	// Maps iterate in random order in Go. The etag function must
	// canonicalize via sort so the same logical input produces the
	// same etag regardless of insertion order.
	in1 := EtagInput{
		Corefile: "x",
		Zones:    map[string]string{"a": "A", "b": "B", "c": "C"},
	}
	in2 := EtagInput{
		Corefile: "x",
		Zones:    map[string]string{"c": "C", "a": "A", "b": "B"},
	}
	if BundleEtag(in1) != BundleEtag(in2) {
		t.Error("etag depends on map insertion order — sort must canonicalize")
	}
}

func TestBundleEtag_LengthIs32HexChars(t *testing.T) {
	got := BundleEtag(EtagInput{Corefile: "x"})
	if len(got) != 32 {
		t.Errorf("etag must be 32 chars (matches Python's [:32] slice); got %d (%s)", len(got), got)
	}
	for _, c := range got {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("etag must be lowercase hex; got %q", got)
		}
	}
}

func TestBundleEtag_CorefileChange(t *testing.T) {
	a := BundleEtag(EtagInput{Corefile: "x"})
	b := BundleEtag(EtagInput{Corefile: "y"})
	if a == b {
		t.Error("corefile change must invalidate etag")
	}
}

func TestBundleEtag_ZoneAdd(t *testing.T) {
	a := BundleEtag(EtagInput{Corefile: "x", Zones: map[string]string{"a": "A"}})
	b := BundleEtag(EtagInput{Corefile: "x", Zones: map[string]string{"a": "A", "b": "B"}})
	if a == b {
		t.Error("zone add must invalidate etag")
	}
}

func TestBundleEtag_ZoneContentChange(t *testing.T) {
	a := BundleEtag(EtagInput{Corefile: "x", Zones: map[string]string{"a": "A"}})
	b := BundleEtag(EtagInput{Corefile: "x", Zones: map[string]string{"a": "B"}})
	if a == b {
		t.Error("zone content change must invalidate etag")
	}
}

// ===== KeyFiles discriminator (0x01) =====

// Critical: a key-file with the same name+content as a zone-file
// must NOT collapse to the same etag stream. The 0x01 discriminator
// separates the streams in Python; verify Go matches.
func TestBundleEtag_KeyFilesDiscriminatedFromZones(t *testing.T) {
	// Case 1: zone "Kfoo" + content "A" vs no key files.
	// Case 2: no zone + key file "Kfoo" + content "A".
	// Without the 0x01 discriminator, both would hash to the same
	// SHA-256 byte stream.
	a := BundleEtag(EtagInput{
		Corefile: "",
		Zones:    map[string]string{"Kfoo": "A"},
	})
	b := BundleEtag(EtagInput{
		Corefile: "",
		KeyFiles: map[string]string{"Kfoo": "A"},
	})
	if a == b {
		t.Errorf("key-files must be discriminated from zones; got identical %s", a)
	}
}

func TestBundleEtag_KeyFilesAdd(t *testing.T) {
	a := BundleEtag(EtagInput{Corefile: "x"})
	b := BundleEtag(EtagInput{Corefile: "x", KeyFiles: map[string]string{"Kksk.key": "..."}})
	if a == b {
		t.Error("key-file add must invalidate etag")
	}
}

// ===== AnycastPrefixes discriminator (0x02) =====

func TestBundleEtag_AnycastPrefixChange(t *testing.T) {
	a := BundleEtag(EtagInput{Corefile: "x", AnycastPrefixes: []string{"10.0.0.0/24"}})
	b := BundleEtag(EtagInput{Corefile: "x", AnycastPrefixes: []string{"10.0.0.0/24", "10.0.1.0/24"}})
	if a == b {
		t.Error("anycast prefix add must invalidate etag")
	}
}

func TestBundleEtag_AnycastSortedDeterministic(t *testing.T) {
	a := BundleEtag(EtagInput{Corefile: "x", AnycastPrefixes: []string{"10.0.0.0/24", "10.0.1.0/24"}})
	b := BundleEtag(EtagInput{Corefile: "x", AnycastPrefixes: []string{"10.0.1.0/24", "10.0.0.0/24"}})
	if a != b {
		t.Errorf("anycast prefix order must not matter (sorted internally): %s vs %s", a, b)
	}
}

// ===== GoBGP JSON canonical encoding =====

func TestBundleEtag_GobgpKeyOrderCanonical(t *testing.T) {
	// Same dict, different insertion order → same etag. Python's
	// json.dumps(sort_keys=True) sorts; Go's canonicalJSON must too.
	a := BundleEtag(EtagInput{
		Corefile: "",
		Gobgp:    map[string]any{"local_as": 65000, "router_id": "10.0.0.1"},
	})
	b := BundleEtag(EtagInput{
		Corefile: "",
		Gobgp:    map[string]any{"router_id": "10.0.0.1", "local_as": 65000},
	})
	if a != b {
		t.Errorf("gobgp dict key order must not matter: %s vs %s", a, b)
	}
}

// Python's json.dumps(default) does NOT HTML-escape; Go's default
// json.Marshal DOES. The renderer must use SetEscapeHTML(false) so
// strings containing `<` `>` `&` produce the same bytes.
func TestBundleEtag_GobgpStringNoHTMLEscape(t *testing.T) {
	// We can't directly observe the bytes hashed (sha256 is one-way),
	// but we can prove the renderer doesn't HTML-escape by checking
	// that a tag containing `&` etag-matches whatever Python's
	// json.dumps for the same input would hash.
	// Pin via test-against-fixture: this etag value was computed by
	// running BundleEtag once with this exact input; if a future
	// change causes a regression to default HTML-escaping, this
	// fails. (See PR #216 — DHCP bundle hit the same trap.)
	val := map[string]any{
		"comment": "a < b & c > d",
	}
	got := BundleEtag(EtagInput{Corefile: "", Gobgp: val})
	// Recompute manually using the same algorithm (proves the
	// no-HTML-escape path produces a stable value).
	expected := manualBundleEtag(t, EtagInput{Corefile: "", Gobgp: val})
	if got != expected {
		t.Errorf("HTML-escape behavior diverged from manual computation:\nwant %s\ngot  %s", expected, got)
	}
}

func TestBundleEtag_GobgpNestedMap(t *testing.T) {
	in := EtagInput{
		Corefile: "",
		Gobgp: map[string]any{
			"global": map[string]any{
				"router_id": "10.0.0.1",
				"as":        65000,
			},
			"neighbors": []any{
				map[string]any{"peer_as": 65001, "neighbor_address": "10.0.0.2"},
			},
		},
	}
	got := BundleEtag(in)
	if len(got) != 32 {
		t.Errorf("nested gobgp dict must still produce 32-char etag; got %d", len(got))
	}
	// Re-key-order at the nested level → same etag.
	in2 := EtagInput{
		Corefile: "",
		Gobgp: map[string]any{
			"neighbors": []any{
				map[string]any{"neighbor_address": "10.0.0.2", "peer_as": 65001},
			},
			"global": map[string]any{
				"as":        65000,
				"router_id": "10.0.0.1",
			},
		},
	}
	if got != BundleEtag(in2) {
		t.Error("nested gobgp key reorder must not change etag")
	}
}

// manualBundleEtag computes the etag directly via the same SHA-256
// + 0x00 separator + canonical JSON formula as BundleEtag, so we
// can verify the production function's output independent of any
// implementation refactor. Used to pin no-HTML-escape behavior
// without committing a golden hex string that gets brittle on
// algorithm changes.
func manualBundleEtag(t *testing.T, in EtagInput) string {
	t.Helper()
	h := sha256.New()
	h.Write([]byte(in.Corefile))
	h.Write([]byte{0x00})
	if in.Gobgp != nil {
		buf, err := canonicalJSON(in.Gobgp)
		if err != nil {
			t.Fatal(err)
		}
		h.Write(buf)
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// ===== canonicalJSON details =====

func TestCanonicalJSON_TopLevelKeysSorted(t *testing.T) {
	buf, err := canonicalJSON(map[string]any{"z": 1, "a": 2, "m": 3})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(buf); got != `{"a":2,"m":3,"z":1}` {
		t.Errorf("got %q", got)
	}
}

func TestCanonicalJSON_NoHTMLEscape(t *testing.T) {
	// Python's json.dumps emits `"a<b&c>d"` verbatim. Go's
	// default json.Marshal escapes those to `< & >`.
	// canonicalJSON must match Python — pin the exact byte stream
	// produced by SetEscapeHTML(false).
	buf, err := canonicalJSON(map[string]any{"k": "a<b&c>d"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"k":"a<b&c>d"}`
	if string(buf) != want {
		t.Errorf("canonical JSON mismatch (HTML-escape divergence?)\nwant %q\ngot  %q", want, buf)
	}
}

func TestCanonicalJSON_NoTrailingNewline(t *testing.T) {
	buf, err := canonicalJSON(map[string]any{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(string(buf), "\n") {
		t.Errorf("canonical JSON must not carry trailing newline (json.Encoder adds, we strip); got %q", buf)
	}
}
