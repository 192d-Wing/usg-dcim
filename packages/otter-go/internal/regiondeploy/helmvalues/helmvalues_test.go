package helmvalues

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fixed UUIDs the testdata fixtures were generated against. The
// fixtures use the same 4-fixture pool for both DNS and DHCP server
// rows — server "id" is 44444444 in both contexts; the renderer
// doesn't share IDs across server types, the fixture *seed* did.
const (
	depID    = "11111111-1111-1111-1111-111111111111"
	siteID   = "22222222-2222-2222-2222-222222222222"
	fabricID = "33333333-3333-3333-3333-333333333333"
	srvID    = "44444444-4444-4444-4444-444444444444"
)

func loadGolden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func assertParity(t *testing.T, fixture string, got string) {
	t.Helper()
	want := loadGolden(t, fixture)
	if got != want {
		t.Errorf("Python parity drift for %s:\n--- want ---\n%s\n--- got ---\n%s\n--- diff at byte %d ---\n%s",
			fixture, want, got, firstDiffOffset(want, got), firstDiffSnippet(want, got))
	}
}

func TestCertManagerValues(t *testing.T) {
	v := RenderCertManagerValues(Deployment{ID: depID})
	out, err := DumpYAML(v)
	if err != nil {
		t.Fatal(err)
	}
	assertParity(t, "cert_manager.yaml", out)
}

func TestDNSAuthValues(t *testing.T) {
	v := RenderDNSAuthValues(Deployment{
		ID:     depID,
		Config: map[string]any{"dns_auth_replicas": 3},
	})
	out, err := DumpYAML(v)
	if err != nil {
		t.Fatal(err)
	}
	assertParity(t, "dns_auth.yaml", out)
}

func TestDNSRecursiveValues_NAT64(t *testing.T) {
	v := RenderDNSRecursiveValues(Deployment{
		ID: depID,
		Config: map[string]any{
			"nat64_enabled":   true,
			"upstream_dns_v6": []string{"2606:4700:4700::1111", "2001:4860:4860::8888"},
		},
	})
	out, err := DumpYAML(v)
	if err != nil {
		t.Fatal(err)
	}
	assertParity(t, "dns_recursive.yaml", out)
}

func TestDNSRecursiveValues_NoNAT64(t *testing.T) {
	v := RenderDNSRecursiveValues(Deployment{ID: depID, Config: map[string]any{}})
	out, err := DumpYAML(v)
	if err != nil {
		t.Fatal(err)
	}
	assertParity(t, "dns_recursive_no_nat64.yaml", out)
}

func TestDHCPValues(t *testing.T) {
	v := RenderDHCPValues(Deployment{
		ID: depID,
		Config: map[string]any{
			"dhcp_scopes": []any{
				map[string]any{
					"subnet": "fd00:dhcp:1::/64",
					"pool":   []any{"fd00:dhcp:1::100-fd00:dhcp:1::200"},
				},
			},
		},
	})
	out, err := DumpYAML(v)
	if err != nil {
		t.Fatal(err)
	}
	assertParity(t, "dhcp.yaml", out)
}

func TestCollectorValues(t *testing.T) {
	v := RenderCollectorValues(Deployment{ID: depID, SiteID: siteID})
	out, err := DumpYAML(v)
	if err != nil {
		t.Fatal(err)
	}
	assertParity(t, "collector.yaml", out)
}

func TestDNSSiteValues_Auth(t *testing.T) {
	v := RenderDNSSiteValues(
		DNSServer{
			ID: srvID, Name: "ns01", Role: "authoritative",
			FabricID: fabricID, SiteID: siteID,
		},
		DNSSiteOptions{
			BundleAPIBaseURL: "https://dcim.example.mil/api/v1/dns",
		},
	)
	out, err := DumpYAML(v)
	if err != nil {
		t.Fatal(err)
	}
	assertParity(t, "dns_site_auth.yaml", out)
}

func TestDNSSiteValues_RecursiveWithAnycast(t *testing.T) {
	v := RenderDNSSiteValues(
		DNSServer{
			ID: srvID, Name: "ns02", Role: "recursive",
			FabricID: fabricID, SiteID: siteID,
		},
		DNSSiteOptions{
			AnycastGroup:         &AnycastGroup{AnycastIPv4: "10.0.0.53", AnycastIPv6: "fd00:any::53"},
			BundleAPIBaseURL:     "https://dcim.example.mil/api/v1/dns",
			BundleCABundleSecret: "dcim-ca-bundle",
			PollSeconds:          30,
			ReplicaCount:         3,
		},
	)
	out, err := DumpYAML(v)
	if err != nil {
		t.Fatal(err)
	}
	assertParity(t, "dns_site_recursive_with_anycast.yaml", out)
}

func TestDHCPSiteValues_Default(t *testing.T) {
	v := RenderDHCPSiteValues(
		DHCPServer{ID: srvID, Name: "dhcp01", FabricID: fabricID},
		DHCPSiteOptions{
			AnycastIPs: []string{"10.0.0.67", "fd00:any::67"},
			DHCPv6:     true,
		},
	)
	out, err := DumpYAML(v)
	if err != nil {
		t.Fatal(err)
	}
	assertParity(t, "dhcp_site_default.yaml", out)
}

func TestDHCPSiteValues_TLS(t *testing.T) {
	v := RenderDHCPSiteValues(
		DHCPServer{ID: srvID, Name: "dhcp01", FabricID: fabricID},
		DHCPSiteOptions{
			AnycastIPs:         []string{"10.0.0.67"},
			DHCPv6:             false,
			CtrlAgentTLSSecret: "kea-tls",
		},
	)
	out, err := DumpYAML(v)
	if err != nil {
		t.Fatal(err)
	}
	assertParity(t, "dhcp_site_tls.yaml", out)
}

// TestExplicitZeroBecomesDefault pins the current Go behavior: an
// explicit 0 in DNSSiteOptions.PollSeconds is overridden to the
// default (60), diverging from Python's keyword-default semantics
// (where `poll_seconds=0` would render `pollSeconds: 0`). Pinning
// the gap so a future refactor that switches to pointer fields will
// trip this test and flag the wire-up sites that need updating.
func TestExplicitZeroBecomesDefault(t *testing.T) {
	v := RenderDNSSiteValues(
		DNSServer{ID: srvID, Name: "ns01", Role: "authoritative", FabricID: fabricID, SiteID: siteID},
		DNSSiteOptions{
			BundleAPIBaseURL: "https://dcim.example.mil/api/v1/dns",
			PollSeconds:      0, // explicit; would emit 0 in Python
			ReplicaCount:     0, // explicit; would emit 0 in Python
		},
	)
	if v.Bundle.PollSeconds != 60 {
		t.Errorf("PollSeconds=0 currently coerces to 60 (Python parity gap); got %d", v.Bundle.PollSeconds)
	}
	if v.ReplicaCount != 1 {
		t.Errorf("ReplicaCount=0 currently coerces to 1 (Python parity gap); got %d", v.ReplicaCount)
	}
}

// TestEmptyAnycastGroup pins the nil-vs-empty AnycastGroup edge —
// a non-nil group with both IPs empty produces no anycast entries.
// Matches Python's `if getattr(group, ...) :` falsy check.
func TestEmptyAnycastGroup(t *testing.T) {
	v := RenderDNSSiteValues(
		DNSServer{ID: srvID, Name: "ns01", Role: "authoritative", FabricID: fabricID, SiteID: siteID},
		DNSSiteOptions{
			AnycastGroup:     &AnycastGroup{},
			BundleAPIBaseURL: "https://dcim.example.mil/api/v1/dns",
		},
	)
	if len(v.Service.AnycastIPs) != 0 {
		t.Errorf("empty AnycastGroup should produce zero anycast IPs; got %v", v.Service.AnycastIPs)
	}
}

// firstDiffOffset returns the byte index of the first divergence.
func firstDiffOffset(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// firstDiffSnippet returns the 80 bytes of `b` starting at the first
// divergence from `a`, for compact error messages.
func firstDiffSnippet(a, b string) string {
	i := firstDiffOffset(a, b)
	start := i - 20
	if start < 0 {
		start = 0
	}
	end := i + 80
	if end > len(b) {
		end = len(b)
	}
	if end > len(a) {
		end = len(a)
	}
	if i >= len(b) {
		return "(b is truncated at byte " + itoa(i) + ")"
	}
	return strings.ReplaceAll(b[start:end], "\n", "\\n")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
