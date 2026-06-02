package dns

import (
	"strings"
	"testing"
)

// ===== patternToRegex =====

func TestPatternToRegex_PlainHost(t *testing.T) {
	got := patternToRegex("evil.example")
	want := `^evil\.example\.?$`
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestPatternToRegex_WildcardHead(t *testing.T) {
	got := patternToRegex("*.evil.example")
	want := `^.+\.evil\.example\.?$`
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestPatternToRegex_TrimsAndLowers(t *testing.T) {
	got := patternToRegex("  Evil.Example.  ")
	want := `^evil\.example\.?$`
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// Python escapes ONLY `.` — every other regex metachar is left raw.
// A pattern with `+` would compile to a regex quantifier; this is
// intentional Python behavior, mirrored verbatim.
func TestPatternToRegex_EscapesOnlyDots(t *testing.T) {
	got := patternToRegex("foo+bar.example")
	if !strings.HasPrefix(got, `^foo+bar\.example`) {
		t.Errorf("`+` should NOT be escaped (Python parity); got %q", got)
	}
}

// ===== RenderBlocklistTemplate =====

func TestRenderBlocklistTemplate_NoPatternsEmpty(t *testing.T) {
	got := RenderBlocklistTemplate(Blocklist{Action: "block"})
	if len(got) != 0 {
		t.Errorf("empty patterns should produce no lines; got %v", got)
	}
}

func TestRenderBlocklistTemplate_BlockGolden(t *testing.T) {
	got := RenderBlocklistTemplate(Blocklist{
		Action:   "block",
		Patterns: []string{"evil.example", "*.bad.test"},
	})
	want := []string{
		"    template ANY ANY {",
		`        match (^evil\.example\.?$)|(^.+\.bad\.test\.?$)`,
		"        rcode NXDOMAIN",
		"    }",
	}
	if len(got) != len(want) {
		t.Fatalf("line count: got %d want %d\nactual: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line[%d]: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestRenderBlocklistTemplate_SinkholeIPv4Only(t *testing.T) {
	v4 := "10.0.0.99"
	got := RenderBlocklistTemplate(Blocklist{
		Action:   "sinkhole",
		Patterns: []string{"evil.example"},
		SinkIPv4: &v4,
	})
	want := []string{
		"    template IN A {",
		`        match (^evil\.example\.?$)`,
		`        answer "{{ .Name }} 60 IN A 10.0.0.99"`,
		"    }",
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("line[%d]: got %q want %q", i, got[i], w)
		}
	}
}

func TestRenderBlocklistTemplate_SinkholeBothFamilies(t *testing.T) {
	v4 := "10.0.0.99"
	v6 := "2001:db8::99"
	got := RenderBlocklistTemplate(Blocklist{
		Action:   "sinkhole",
		Patterns: []string{"evil.example"},
		SinkIPv4: &v4, SinkIPv6: &v6,
	})
	// 4 lines for A + 4 for AAAA = 8.
	if len(got) != 8 {
		t.Fatalf("want 8 lines for dual-stack sinkhole; got %d:\n%v", len(got), got)
	}
	// A block precedes AAAA block.
	idxA := -1
	idxAAAA := -1
	for i, line := range got {
		if strings.Contains(line, "template IN A {") {
			idxA = i
		}
		if strings.Contains(line, "template IN AAAA {") {
			idxAAAA = i
		}
	}
	if idxA < 0 || idxAAAA < 0 || idxA >= idxAAAA {
		t.Errorf("A must precede AAAA: idxA=%d idxAAAA=%d", idxA, idxAAAA)
	}
}

func TestRenderBlocklistTemplate_SinkholeNoSinkIPsEmpty(t *testing.T) {
	got := RenderBlocklistTemplate(Blocklist{
		Action:   "sinkhole",
		Patterns: []string{"evil.example"},
	})
	if len(got) != 0 {
		t.Errorf("sinkhole with no sink IPs should produce no lines; got %v", got)
	}
}

func TestRenderBlocklistTemplate_UnknownActionEmpty(t *testing.T) {
	got := RenderBlocklistTemplate(Blocklist{
		Action: "unknown", Patterns: []string{"evil.example"},
	})
	if len(got) != 0 {
		t.Errorf("unknown action should produce no lines; got %v", got)
	}
}

// ===== RenderCorefileRecursive =====

func TestRenderCorefileRecursive_MinimalCatchallGolden(t *testing.T) {
	got := RenderCorefileRecursive(CorefileRecursiveInput{})
	want := ".:53 {\n" +
		"    forward . 1.1.1.1 8.8.8.8\n" +
		"    cache 300\n" +
		"    log\n" +
		"    errors\n" +
		"    prometheus :9153\n" +
		"    health :8080\n" +
		"}\n"
	if got != want {
		t.Errorf("minimal catchall golden mismatch\nwant %q\ngot  %q", want, got)
	}
}

func TestRenderCorefileRecursive_CustomUpstreams(t *testing.T) {
	got := RenderCorefileRecursive(CorefileRecursiveInput{
		UpstreamResolvers: []string{"9.9.9.9", "149.112.112.112"},
	})
	if !strings.Contains(got, "forward . 9.9.9.9 149.112.112.112") {
		t.Errorf("custom upstreams not threaded; got %q", got)
	}
}

// Per-fabric stub-zone forwards: one block per apex when
// AuthUnicastIP is set. Apexes sorted + deduped.
func TestRenderCorefileRecursive_PerApexStubZoneForwards(t *testing.T) {
	authIP := "10.0.0.1"
	got := RenderCorefileRecursive(CorefileRecursiveInput{
		FabricApexes:  []string{"b.example.", "a.example.", "a.example."},
		AuthUnicastIP: &authIP,
	})
	// Dedup keeps a.example. once, sort puts it before b.example.
	idxA := strings.Index(got, "a.example.:53")
	idxB := strings.Index(got, "b.example.:53")
	if idxA < 0 || idxB < 0 || idxA >= idxB {
		t.Errorf("apex blocks not sorted: a=%d b=%d", idxA, idxB)
	}
	// Forward target points at the auth IP.
	if !strings.Contains(got, "forward . 10.0.0.1:53") {
		t.Errorf("auth IP not in apex forward; got %q", got)
	}
	// Catch-all block appears LAST.
	idxCatchall := strings.LastIndex(got, ".:53 {")
	if idxCatchall < idxB {
		t.Errorf("catchall must follow apex blocks: catchall=%d b=%d", idxCatchall, idxB)
	}
}

func TestRenderCorefileRecursive_NoAuthIPSkipsApexBlocks(t *testing.T) {
	got := RenderCorefileRecursive(CorefileRecursiveInput{
		FabricApexes: []string{"a.example.", "b.example."},
	})
	if strings.Contains(got, "a.example.:53") {
		t.Errorf("AuthUnicastIP nil must skip per-apex blocks; got %q", got)
	}
}

func TestRenderCorefileRecursive_ConditionalForwardersSortedAndSkipEmpty(t *testing.T) {
	got := RenderCorefileRecursive(CorefileRecursiveInput{
		ConditionalForwarders: []ConditionalForwarder{
			{Pattern: "z.test.", Upstreams: []string{"1.2.3.4"}},
			{Pattern: "a.test.", Upstreams: []string{}}, // empty → skipped
			{Pattern: "m.test.", Upstreams: []string{"5.6.7.8"}},
		},
	})
	idxM := strings.Index(got, "m.test.:53")
	idxZ := strings.Index(got, "z.test.:53")
	if idxM < 0 || idxZ < 0 || idxM >= idxZ {
		t.Errorf("conditional forwarders not sorted: m=%d z=%d", idxM, idxZ)
	}
	if strings.Contains(got, "a.test.:53") {
		t.Errorf("empty-upstream forwarder must be skipped; got %q", got)
	}
}

// Blocklists inside the catch-all block, applied in caller order.
func TestRenderCorefileRecursive_BlocklistsInCatchall(t *testing.T) {
	v4 := "10.0.0.99"
	got := RenderCorefileRecursive(CorefileRecursiveInput{
		Blocklists: []Blocklist{
			{Action: "block", Patterns: []string{"bad.test"}},
			{Action: "sinkhole", Patterns: []string{"phish.test"}, SinkIPv4: &v4},
		},
	})
	// block template precedes sinkhole template (caller order).
	idxBlock := strings.Index(got, "template ANY ANY {")
	idxSink := strings.Index(got, "template IN A {")
	if idxBlock < 0 || idxSink < 0 || idxBlock >= idxSink {
		t.Errorf("block must precede sinkhole (caller order): block=%d sink=%d", idxBlock, idxSink)
	}
	// Both inside the catch-all block (precede `forward . `).
	idxForward := strings.Index(got, "    forward . ")
	if idxBlock >= idxForward || idxSink >= idxForward {
		t.Errorf("templates must precede catchall forward: block=%d sink=%d forward=%d",
			idxBlock, idxSink, idxForward)
	}
}

// Determinism across calls with the same input — required for etag.
func TestRenderCorefileRecursive_Deterministic(t *testing.T) {
	authIP := "10.0.0.1"
	in := CorefileRecursiveInput{
		FabricApexes:      []string{"a.example.", "b.example."},
		AuthUnicastIP:     &authIP,
		UpstreamResolvers: []string{"9.9.9.9"},
		ConditionalForwarders: []ConditionalForwarder{
			{Pattern: "m.test.", Upstreams: []string{"1.1.1.1"}},
		},
	}
	if RenderCorefileRecursive(in) != RenderCorefileRecursive(in) {
		t.Error("recursive Corefile not deterministic across calls")
	}
}
