package dbq

import (
	"encoding/json"
	"strings"
	"testing"
)

// Pins the wire-shape contract for jsonb columns. Python otter's
// FastAPI returns jsonb fields as JSON objects (or null). The first
// Go port typed them as []byte, which Go's encoding/json
// base64-encodes — so finch would have seen
// "metadata_json": "eyJrZX..." instead of "metadata_json": {"key": ...}.
//
// Switching to json.RawMessage makes the field encode as itself.
// If anyone reverts to []byte, this test fails — and the JSON in
// the failure message makes the regression obvious.
func TestJsonbFieldsEncodeAsObjects(t *testing.T) {
	site := Site{MetadataJson: json.RawMessage(`{"owner":"acme"}`)}
	asset := Asset{MetadataJson: json.RawMessage(`{"firmware":"v2"}`)}

	for _, tc := range []struct {
		name string
		v    any
		want string
	}{
		{"site", site, `"metadata_json":{"owner":"acme"}`},
		{"asset", asset, `"metadata_json":{"firmware":"v2"}`},
	} {
		raw, err := json.Marshal(tc.v)
		if err != nil {
			t.Fatalf("%s marshal: %v", tc.name, err)
		}
		if !strings.Contains(string(raw), tc.want) {
			t.Errorf("%s: want substring %q, got %s", tc.name, tc.want, raw)
		}
		// Base64 always starts with letter chars; the regression would
		// emit a string-typed field. Detect by asserting no quote-then-
		// alphanum-only pattern right after the key.
		if strings.Contains(string(raw), `"metadata_json":"`) {
			t.Errorf("%s: jsonb base64-encoded as string — typed as []byte instead of json.RawMessage? body=%s", tc.name, raw)
		}
	}
}

// Null jsonb (nil RawMessage) must serialize as null, not as the
// invalid string "" — that would break finch's JSON parse.
func TestJsonbFieldEncodesNullForZeroValue(t *testing.T) {
	raw, err := json.Marshal(Site{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"metadata_json":null`) {
		t.Errorf("zero-value jsonb should be null, got %s", raw)
	}
}
