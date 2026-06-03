package dns

import (
	"strings"
	"testing"
)

// ===== splitHostPort =====

func TestSplitHostPort_BareIP(t *testing.T) {
	h, p := splitHostPort("10.0.0.1", 53)
	if h != "10.0.0.1" || p != 53 {
		t.Errorf("got %q %d", h, p)
	}
}

func TestSplitHostPort_IPv4WithPort(t *testing.T) {
	h, p := splitHostPort("10.0.0.1:5353", 53)
	if h != "10.0.0.1" || p != 5353 {
		t.Errorf("got %q %d", h, p)
	}
}

func TestSplitHostPort_IPv6Bracketless(t *testing.T) {
	h, p := splitHostPort("2001:db8::1", 53)
	if h != "2001:db8::1" || p != 53 {
		t.Errorf("got %q %d (IPv6 without brackets must NOT be split on :)", h, p)
	}
}

func TestSplitHostPort_IPv6BracketedWithPort(t *testing.T) {
	h, p := splitHostPort("[::1]:5353", 53)
	if h != "::1" || p != 5353 {
		t.Errorf("got %q %d", h, p)
	}
}

func TestSplitHostPort_IPv6BracketedNoPort(t *testing.T) {
	h, p := splitHostPort("[::1]", 53)
	if h != "::1" || p != 53 {
		t.Errorf("got %q %d", h, p)
	}
}

func TestSplitHostPort_BadPortFallsBack(t *testing.T) {
	// Python returns (target, default_port) on parse failure — the
	// full untouched target, not the host portion. Mirror that
	// exactly so wire output matches.
	h, p := splitHostPort("10.0.0.1:notnum", 53)
	if h != "10.0.0.1:notnum" || p != 53 {
		t.Errorf("got %q %d (Python returns full target on bad port)", h, p)
	}
}

// ===== hickoryAclLines =====

func TestHickoryAclLines_EmptyEmits(t *testing.T) {
	if got := hickoryAclLines(nil, nil, false); got != nil {
		t.Errorf("empty inputs should emit no lines; got %v", got)
	}
}

func TestHickoryAclLines_DenyOnly(t *testing.T) {
	got := hickoryAclLines([]string{"10.0.0.0/8"}, nil, false)
	want := []string{`deny_networks = ["10.0.0.0/8"]`, ""}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestHickoryAclLines_StrictOnlyWhenAllowSet(t *testing.T) {
	// strict=true + allow=empty should NOT emit allow_networks_strict
	// (Python rationale: the flag has no effect without allow rules).
	got := hickoryAclLines([]string{"10.0.0.0/8"}, nil, true)
	for _, line := range got {
		if strings.HasPrefix(line, "allow_networks_strict") {
			t.Errorf("allow_networks_strict should NOT emit when allow is empty; got %v", got)
		}
	}
}

func TestHickoryAclLines_StrictWithAllow(t *testing.T) {
	got := hickoryAclLines(nil, []string{"10.0.0.0/8"}, true)
	found := false
	for _, line := range got {
		if line == "allow_networks_strict = true" {
			found = true
		}
	}
	if !found {
		t.Errorf("allow_networks_strict missing; got %v", got)
	}
}

// ===== hickoryTLSLines =====

func TestHickoryTLSLines_EmptyWithoutCert(t *testing.T) {
	got := hickoryTLSLines(HickoryRecursiveInput{DoTEnabled: true})
	if got != nil {
		t.Errorf("no cert should suppress emission; got %v", got)
	}
}

func TestHickoryTLSLines_EmptyWithoutListenerFlag(t *testing.T) {
	got := hickoryTLSLines(HickoryRecursiveInput{
		TLSCertPath: "/c", TLSKeyPath: "/k",
	})
	if got != nil {
		t.Errorf("no DoT/DoH should suppress emission; got %v", got)
	}
}

func TestHickoryTLSLines_DoTAndDoH(t *testing.T) {
	got := hickoryTLSLines(HickoryRecursiveInput{
		TLSCertPath: "/c", TLSKeyPath: "/k",
		DoTEnabled: true, DoHEnabled: true,
		TLSListenPort: 853, HTTPSListenPort: 443, DoHPath: "/dns-query",
	})
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "tls_listen_port = 853") {
		t.Errorf("DoT port missing; got %s", joined)
	}
	if !strings.Contains(joined, "https_listen_port = 443") {
		t.Errorf("DoH port missing; got %s", joined)
	}
	if !strings.Contains(joined, `http_endpoint = "/dns-query"`) {
		t.Errorf("DoH path missing; got %s", joined)
	}
	if !strings.Contains(joined, `path = "/c"`) || !strings.Contains(joined, `private_key = "/k"`) {
		t.Errorf("TLS cert/key paths missing; got %s", joined)
	}
}

// ===== RenderHickoryRecursiveConfig =====

func TestRenderHickoryRecursive_MinimalCatchall(t *testing.T) {
	got := RenderHickoryRecursiveConfig(HickoryRecursiveInput{})
	if !strings.Contains(got, `listen_addrs_ipv4 = ["0.0.0.0"]`) {
		t.Errorf("missing v4 listener; got %q", got)
	}
	if !strings.Contains(got, `listen_addrs_ipv6 = ["::"]`) {
		t.Errorf("missing v6 listener; got %q", got)
	}
	// Default upstreams when none configured.
	if !strings.Contains(got, `ip = "1.1.1.1"`) || !strings.Contains(got, `ip = "8.8.8.8"`) {
		t.Errorf("default upstreams not threaded; got %q", got)
	}
}

func TestRenderHickoryRecursive_FabricApexForwardZone(t *testing.T) {
	got := RenderHickoryRecursiveConfig(HickoryRecursiveInput{
		FabricApexes:  []string{"site.example."},
		AuthUnicastIP: sptr("10.0.0.1"),
	})
	if !strings.Contains(got, `zone = "site.example."`) {
		t.Errorf("apex zone missing; got %q", got)
	}
	if !strings.Contains(got, `ip = "10.0.0.1"`) {
		t.Errorf("auth IP not threaded; got %q", got)
	}
}

func TestRenderHickoryRecursive_ConditionalForwardersSortedAndSkipEmpty(t *testing.T) {
	got := RenderHickoryRecursiveConfig(HickoryRecursiveInput{
		ConditionalForwarders: []ConditionalForwarder{
			{Pattern: "z.test.", Upstreams: []string{"1.2.3.4"}},
			{Pattern: "a.test.", Upstreams: []string{}}, // skipped
			{Pattern: "m.test.", Upstreams: []string{"5.6.7.8"}},
		},
	})
	idxM := strings.Index(got, `zone = "m.test."`)
	idxZ := strings.Index(got, `zone = "z.test."`)
	if idxM < 0 || idxZ < 0 || idxM >= idxZ {
		t.Errorf("conditional forwarders not sorted: m=%d z=%d", idxM, idxZ)
	}
	if strings.Contains(got, `zone = "a.test."`) {
		t.Errorf("empty-upstream forwarder must be skipped; got %q", got)
	}
}

func TestRenderHickoryRecursive_RPZAndResponsePolicy(t *testing.T) {
	got := RenderHickoryRecursiveConfig(HickoryRecursiveInput{
		RpzZoneRefs: []RPZRef{
			{Name: "bl-001.rpz.dcim.local", Filename: "bl-001.rpz.dcim.local.zone"},
		},
	})
	if !strings.Contains(got, `zone_type = "Primary"`) {
		t.Errorf("RPZ Primary zone missing; got %q", got)
	}
	if !strings.Contains(got, `[[response_policy]]`) {
		t.Errorf("response_policy block missing; got %q", got)
	}
	if !strings.Contains(got, `zones = ["bl-001.rpz.dcim.local."]`) {
		t.Errorf("response_policy zones array missing trailing-dot label; got %q", got)
	}
}

func TestRenderHickoryRecursive_PrometheusEmittedOnAddr(t *testing.T) {
	got := RenderHickoryRecursiveConfig(HickoryRecursiveInput{
		PrometheusListenAddr: "0.0.0.0:9153",
	})
	if !strings.Contains(got, `prometheus_listen_addr = "0.0.0.0:9153"`) {
		t.Errorf("prometheus addr missing; got %q", got)
	}
}

func TestRenderHickoryRecursive_Deterministic(t *testing.T) {
	in := HickoryRecursiveInput{
		FabricApexes:      []string{"a.example.", "b.example."},
		AuthUnicastIP:     sptr("10.0.0.1"),
		UpstreamResolvers: []string{"9.9.9.9"},
		ConditionalForwarders: []ConditionalForwarder{
			{Pattern: "m.test.", Upstreams: []string{"1.1.1.1"}},
		},
		RpzZoneRefs: []RPZRef{
			{Name: "bl-001.rpz.dcim.local", Filename: "bl-001.rpz.dcim.local.zone"},
		},
	}
	// Two separate calls with identical input must produce identical
	// output for etag stability. Captured to distinct variables so
	// SonarCloud doesn't flag the comparison as a self-comparison.
	a := RenderHickoryRecursiveConfig(in)
	b := RenderHickoryRecursiveConfig(in)
	if a != b {
		t.Error("renderer not deterministic across calls")
	}
}
