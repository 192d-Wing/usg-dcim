// Unit tests for the lease parsing helpers. The orchestrator tests
// in PR 15 cover the end-to-end pipeline; these pin the per-helper
// contract that Python and Go must agree on.
package kea

import (
	"encoding/json"
	"testing"
	"time"
)

func TestLeaseValidUntil_HappyPath(t *testing.T) {
	cltt := int64(1700000000)   // 2023-11-14T22:13:20Z
	validLft := int64(86400)    // 1 day
	got := LeaseValidUntil(&cltt, &validLft)
	if got == nil {
		t.Fatal("got nil expiry")
	}
	want := time.Unix(cltt, 0).UTC().Add(24 * time.Hour)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestLeaseValidUntil_NilInputs(t *testing.T) {
	if got := LeaseValidUntil(nil, nil); got != nil {
		t.Errorf("both nil: got %v, want nil", got)
	}
	cltt := int64(1700000000)
	if got := LeaseValidUntil(&cltt, nil); got != nil {
		t.Errorf("nil valid_lft: got %v, want nil", got)
	}
	validLft := int64(60)
	if got := LeaseValidUntil(nil, &validLft); got != nil {
		t.Errorf("nil cltt: got %v, want nil", got)
	}
}

func TestLeaseValidUntil_NegativeRejected(t *testing.T) {
	cltt := int64(-1)
	validLft := int64(60)
	if got := LeaseValidUntil(&cltt, &validLft); got != nil {
		t.Errorf("negative cltt: got %v, want nil", got)
	}
	cltt = int64(1700000000)
	validLft = int64(-60)
	if got := LeaseValidUntil(&cltt, &validLft); got != nil {
		t.Errorf("negative valid_lft: got %v, want nil", got)
	}
}

func TestParseLease_V4HappyPath(t *testing.T) {
	raw := map[string]any{
		"ip-address":  "10.0.0.5",
		"hw-address":  "aa:bb:cc:dd:ee:01",
		"hostname":    "host-1",
		"cltt":        float64(1700000000),
		"valid-lft":   float64(86400),
		"state":       float64(0),
	}
	got := ParseLease(raw)
	if got == nil {
		t.Fatal("got nil")
	}
	if got.Address != "10.0.0.5" || got.MAC != "aa:bb:cc:dd:ee:01" || got.Hostname != "host-1" {
		t.Errorf("parsed = %+v", got)
	}
	if got.ValidUntil == nil {
		t.Errorf("ValidUntil nil; should have been computed from cltt+valid-lft")
	}
}

func TestParseLease_V6FallsBackToDuid(t *testing.T) {
	// Python at services/kea.py:79: when hw-address is absent (v6
	// case) use duid instead.
	raw := map[string]any{
		"ip-address": "2001:db8::1",
		"duid":       "00:01:00:01:abcd",
		"state":      float64(0),
	}
	got := ParseLease(raw)
	if got == nil {
		t.Fatal("got nil")
	}
	if got.MAC != "00:01:00:01:abcd" {
		t.Errorf("MAC = %q, want duid fallback", got.MAC)
	}
}

func TestParseLease_DeclinedSkipped(t *testing.T) {
	// state=1 (declined) and state=2 (expired-reclaimed) are both
	// dropped — they don't represent an active binding.
	for _, state := range []float64{1, 2} {
		raw := map[string]any{
			"ip-address": "10.0.0.5",
			"hw-address": "aa:bb:cc:dd:ee:01",
			"state":      state,
		}
		got := ParseLease(raw)
		if got != nil {
			t.Errorf("state=%v: expected skip, got %+v", state, got)
		}
	}
}

func TestParseLease_MissingIPAddressSkipped(t *testing.T) {
	raw := map[string]any{
		"hw-address": "aa:bb:cc:dd:ee:01",
		"state":      float64(0),
	}
	if got := ParseLease(raw); got != nil {
		t.Errorf("missing ip-address: expected skip, got %+v", got)
	}
}

func TestParseLease_WhitespaceHostnameNormalized(t *testing.T) {
	// Kea sometimes ships just whitespace for "client didn't send
	// one"; the parser trims so downstream IPAddress.dns_name
	// writes don't store noise.
	raw := map[string]any{
		"ip-address": "10.0.0.5",
		"hw-address": "aa:bb:cc:dd:ee:01",
		"hostname":   "   ",
		"state":      float64(0),
	}
	got := ParseLease(raw)
	if got == nil || got.Hostname != "" {
		t.Errorf("hostname = %q, want empty after trim", got.Hostname)
	}
}

func TestExtractLeases_HappyPath(t *testing.T) {
	body := []map[string]any{{
		"result": float64(0),
		"arguments": map[string]any{
			"leases": []any{
				map[string]any{"ip-address": "10.0.0.5"},
				map[string]any{"ip-address": "10.0.0.6"},
			},
		},
	}}
	raw, _ := json.Marshal(body)
	got := ExtractLeases(raw)
	if len(got) != 2 {
		t.Errorf("got %d leases, want 2", len(got))
	}
}

func TestExtractLeases_EmptyResultCodeAccepted(t *testing.T) {
	// result=3 means "no leases", not an error — Python parity at
	// services/kea.py:214.
	body := []map[string]any{{
		"result":    float64(3),
		"arguments": map[string]any{"leases": []any{}},
	}}
	raw, _ := json.Marshal(body)
	got := ExtractLeases(raw)
	if got != nil && len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestExtractLeases_ErrorCodeSkipped(t *testing.T) {
	body := []map[string]any{
		{"result": float64(1), "text": "kea blew up"},
		// A second service that returned leases successfully —
		// still extracted, the bad service is silently dropped.
		{
			"result":    float64(0),
			"arguments": map[string]any{"leases": []any{map[string]any{"ip-address": "10.0.0.5"}}},
		},
	}
	raw, _ := json.Marshal(body)
	got := ExtractLeases(raw)
	if len(got) != 1 {
		t.Errorf("got %d leases, want 1 (error-coded service contributes 0)", len(got))
	}
}

func TestExtractLeases_MalformedShapeReturnsEmpty(t *testing.T) {
	got := ExtractLeases([]byte(`{"not":"an array"}`))
	if got != nil {
		t.Errorf("got %v, want nil for malformed input", got)
	}
}

func TestExtractLeases_MissingArgumentsDropped(t *testing.T) {
	body := []map[string]any{{
		"result": float64(0),
	}}
	raw, _ := json.Marshal(body)
	got := ExtractLeases(raw)
	if len(got) != 0 {
		t.Errorf("got %d, want 0", len(got))
	}
}
