package bundle

import (
	"encoding/json"
	"testing"
)

func TestEncodeForCache_ProducesValidJSON(t *testing.T) {
	// The whole point of EncodeForCache is to land valid JSON bytes
	// in the JSONB bundle_cache_json column. JSONB normalization
	// means we can't pin byte equality — keys reorder, whitespace
	// vanishes, trailing newlines drop. Pin the semantic shape
	// instead: every top-level KeaBundle field survives the
	// round-trip.
	b := KeaBundle{
		ServerID:  "abc-123",
		CtrlAgent: map[string]any{"http-port": float64(8000)},
		Dhcp4:     map[string]any{"subnet4": []any{}},
		Dhcp6:     map[string]any{"subnet6": []any{}},
		Etag:      "deadbeefcafebabe",
	}
	encoded, err := EncodeForCache(b)
	if err != nil {
		t.Fatalf("EncodeForCache: %v", err)
	}
	var got KeaBundle
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("encoded bytes don't round-trip as KeaBundle: %v", err)
	}
	if got.ServerID != b.ServerID {
		t.Errorf("ServerID: got %q, want %q", got.ServerID, b.ServerID)
	}
	if got.Etag != b.Etag {
		t.Errorf("Etag: got %q, want %q", got.Etag, b.Etag)
	}
	if got.CtrlAgent["http-port"] != float64(8000) {
		t.Errorf("CtrlAgent.http-port survived round-trip wrong: got %v", got.CtrlAgent["http-port"])
	}
}

func TestEncodeForCache_NoTrailingNewline(t *testing.T) {
	// json.Marshal does not append a trailing '\n' (unlike
	// json.Encoder.Encode). This is intentional — Postgres JSONB
	// strips trailing whitespace anyway, so the encoder-trailing-\n
	// would only show up before the INSERT and never on read.
	// Using Marshal keeps the byte stream simpler and avoids the
	// false sense that the trailing \n round-trips through the
	// column.
	encoded, err := EncodeForCache(KeaBundle{ServerID: "x"})
	if err != nil {
		t.Fatalf("EncodeForCache: %v", err)
	}
	if len(encoded) == 0 {
		t.Fatalf("encoded is empty")
	}
	if encoded[len(encoded)-1] == '\n' {
		t.Errorf("EncodeForCache should not append trailing '\\n'; got %q", encoded)
	}
}
