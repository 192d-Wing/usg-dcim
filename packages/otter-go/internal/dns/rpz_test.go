package dns

import (
	"strings"
	"testing"
	"time"
)

func sptr(s string) *string { return &s }

// ===== RenderRPZZone =====

func TestRenderRPZZone_EmptyPatternsEmpty(t *testing.T) {
	got := RenderRPZZone(RPZInput{
		RpzZoneName: "bl.rpz.local",
		Action:      "block",
		Patterns:    nil,
		Now:         time.Unix(1700000000, 0).UTC(),
	})
	if got != "" {
		t.Errorf("empty patterns should produce empty output; got %q", got)
	}
}

func TestRenderRPZZone_SinkholeWithNoSinkIPsEmpty(t *testing.T) {
	got := RenderRPZZone(RPZInput{
		RpzZoneName: "bl.rpz.local",
		Action:      "sinkhole",
		Patterns:    []string{"evil.example"},
		Now:         time.Unix(1700000000, 0).UTC(),
	})
	if got != "" {
		t.Errorf("sinkhole with no sink IPs should produce empty output; got %q", got)
	}
}

// Golden: block action with two patterns. Pins SOA serial from the
// caller-supplied `Now` so the test is reproducible; the rest of
// the output (apex, NS, TTL, owner records) is verbatim with Python.
func TestRenderRPZZone_BlockGolden(t *testing.T) {
	got := RenderRPZZone(RPZInput{
		RpzZoneName: "bl-001.rpz.dcim.local",
		Action:      "block",
		Patterns:    []string{"foo.example", "bar.example"},
		Now:         time.Unix(1700000000, 0).UTC(),
	})
	want := "$ORIGIN bl-001.rpz.dcim.local.\n" +
		"$TTL 60\n" +
		"@\tIN\tSOA\tns1.bl-001.rpz.dcim.local. hostmaster.bl-001.rpz.dcim.local. (\n" +
		"\t\t\t1700000000\t; serial\n" +
		"\t\t\t900\t; refresh\n" +
		"\t\t\t900\t; retry\n" +
		"\t\t\t1800\t; expire\n" +
		"\t\t\t60)\t; minimum\n" +
		"@\t300\tIN\tNS\tns1.bl-001.rpz.dcim.local.\n" +
		"\n" +
		"bar.example\tIN\tCNAME\t.\n" +
		"foo.example\tIN\tCNAME\t.\n" +
		"\n"
	if got != want {
		t.Errorf("RPZ block golden mismatch\nwant %q\ngot  %q", want, got)
	}
}

// Sinkhole with both IPv4 + IPv6 sinks: every pattern gets both A
// and AAAA records.
func TestRenderRPZZone_SinkholeBothFamilies(t *testing.T) {
	got := RenderRPZZone(RPZInput{
		RpzZoneName: "bl.rpz.local",
		Action:      "sinkhole",
		Patterns:    []string{"evil.example"},
		SinkIPv4:    "10.0.0.99",
		SinkIPv6:    "2001:db8::99",
		Now:         time.Unix(1700000000, 0).UTC(),
	})
	if !strings.Contains(got, "evil.example\tIN\tA\t10.0.0.99\n") {
		t.Errorf("missing A record; got %q", got)
	}
	if !strings.Contains(got, "evil.example\tIN\tAAAA\t2001:db8::99\n") {
		t.Errorf("missing AAAA record; got %q", got)
	}
}

func TestRenderRPZZone_PatternsSortedAndDeduped(t *testing.T) {
	// "evil.example" appears twice (one with whitespace) — must dedupe.
	got := RenderRPZZone(RPZInput{
		RpzZoneName: "bl.rpz.local",
		Action:      "block",
		Patterns:    []string{"  evil.example  ", "ace.example", "evil.example", ""},
		Now:         time.Unix(1700000000, 0).UTC(),
	})
	// ace before evil; each only once.
	idxAce := strings.Index(got, "ace.example")
	idxEvil := strings.Index(got, "evil.example")
	if idxAce < 0 || idxEvil < 0 || idxAce >= idxEvil {
		t.Errorf("patterns must sort alphabetically; got %q", got)
	}
	if strings.Count(got, "evil.example") != 1 {
		t.Errorf("evil.example must appear exactly once (deduped); got %d", strings.Count(got, "evil.example"))
	}
}

func TestRenderRPZZone_PatternTrailingDotStripped(t *testing.T) {
	got := RenderRPZZone(RPZInput{
		RpzZoneName: "bl.rpz.local",
		Action:      "block",
		Patterns:    []string{"evil.example."},
		Now:         time.Unix(1700000000, 0).UTC(),
	})
	if !strings.Contains(got, "evil.example\tIN\tCNAME\t.\n") {
		t.Errorf("trailing dot should be stripped on owner; got %q", got)
	}
	if strings.Contains(got, "evil.example.\tIN") {
		t.Errorf("owner should not retain trailing dot; got %q", got)
	}
}

func TestRenderRPZZone_DefaultTTLOverride(t *testing.T) {
	got := RenderRPZZone(RPZInput{
		RpzZoneName: "bl.rpz.local",
		Action:      "block",
		Patterns:    []string{"x.example"},
		DefaultTTL:  300,
		Now:         time.Unix(1700000000, 0).UTC(),
	})
	if !strings.Contains(got, "$TTL 300\n") {
		t.Errorf("custom $TTL not threaded; got %q", got)
	}
	if !strings.Contains(got, "\t\t\t300)\t; minimum\n") {
		t.Errorf("custom SOA minimum not threaded; got %q", got)
	}
}

// ===== BuildRpzArtifacts =====

func TestBuildRpzArtifacts_PredictableNaming(t *testing.T) {
	bls := []Blocklist{
		{Action: "block", Patterns: []string{"a.example"}},
		{Action: "block", Patterns: []string{"b.example"}},
	}
	zones, refs := BuildRpzArtifacts(bls, time.Unix(1700000000, 0).UTC())
	if len(zones) != 2 {
		t.Fatalf("want 2 zones; got %d (%v)", len(zones), zones)
	}
	if _, ok := zones["bl-000.rpz.dcim.local.zone"]; !ok {
		t.Errorf("missing bl-000 zone")
	}
	if _, ok := zones["bl-001.rpz.dcim.local.zone"]; !ok {
		t.Errorf("missing bl-001 zone")
	}
	if len(refs) != 2 || refs[0].Name != "bl-000.rpz.dcim.local" {
		t.Errorf("ref ordering wrong: %+v", refs)
	}
}

func TestBuildRpzArtifacts_SkipsEmptyPatterns(t *testing.T) {
	bls := []Blocklist{
		{Action: "block", Patterns: []string{}},                       // skipped
		{Action: "block", Patterns: []string{"x.example"}},            // index 1
		{Action: "sinkhole", Patterns: []string{"y.example"}},         // skipped (no sinks)
		{Action: "sinkhole", Patterns: []string{"z.example"}, SinkIPv4: sptr("10.0.0.1")}, // index 3
	}
	zones, refs := BuildRpzArtifacts(bls, time.Unix(1700000000, 0).UTC())
	if len(zones) != 2 {
		t.Fatalf("want 2 zones; got %d", len(zones))
	}
	// Predictable index preservation: bl-001 + bl-003, NOT renumbered.
	if _, ok := zones["bl-001.rpz.dcim.local.zone"]; !ok {
		t.Errorf("expected bl-001 (preserve original index)")
	}
	if _, ok := zones["bl-003.rpz.dcim.local.zone"]; !ok {
		t.Errorf("expected bl-003 (preserve original index)")
	}
	if len(refs) != 2 {
		t.Errorf("refs count: got %d want 2", len(refs))
	}
}
