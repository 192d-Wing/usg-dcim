// Metrics-emission tests.
//
// We don't try to spin up a full Prometheus scrape — just verify
// the counters increment on the events they're supposed to. This
// catches regressions where signMessage / attachDenialProof get
// refactored without re-wiring the metric calls.

package nsec3sign

import (
	"context"
	"testing"

	"github.com/coredns/coredns/plugin/pkg/cache"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/miekg/dns"
)

func TestCacheHitMetricsIncrement(t *testing.T) {
	const zone = "example.test."
	const server = "" // metrics.WithServer returns "" outside a real CoreDNS context

	a := &dns.A{
		Hdr: dns.RR_Header{Name: "host." + zone, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
		A:   []byte{10, 0, 0, 1},
	}
	n, _ := newPluginWithKey(t, zone, 256, []dns.RR{a}, nil)
	n.SigCache = cache.New(8)

	missBefore := testutil.ToFloat64(cacheMisses.WithLabelValues(server))
	hitBefore := testutil.ToFloat64(cacheHits.WithLabelValues(server))

	// First call: miss.
	w := &captureWriter{}
	if _, err := n.ServeDNS(context.Background(), w, query("host."+zone, dns.TypeA, true)); err != nil {
		t.Fatal(err)
	}
	// Second call: hit.
	if _, err := n.ServeDNS(context.Background(), w, query("host."+zone, dns.TypeA, true)); err != nil {
		t.Fatal(err)
	}

	missAfter := testutil.ToFloat64(cacheMisses.WithLabelValues(server))
	hitAfter := testutil.ToFloat64(cacheHits.WithLabelValues(server))

	if missAfter <= missBefore {
		t.Errorf("cache_misses_total didn't advance: %f → %f", missBefore, missAfter)
	}
	if hitAfter <= hitBefore {
		t.Errorf("cache_hits_total didn't advance: %f → %f", hitBefore, hitAfter)
	}
}

func TestDenialMetricsIncrement(t *testing.T) {
	const zone = "example.test."
	const server = ""

	zskDK, zskPriv := generateDNSKEY(t, zone, dns.ECDSAP256SHA256, 256, 256)
	zsk := &signingKey{KeyTag: zskDK.KeyTag(), Public: zskDK, Private: zskPriv}
	n := &Nsec3Sign{
		Zones: []string{zone},
		Keys:  []*signingKey{zsk},
		Chain: tinyChain(),
		Next:  &nxdomainHandler{soa: soaFor(zone)},
	}

	before := testutil.ToFloat64(denialsIssued.WithLabelValues(server, "nxdomain"))
	w := &captureWriter{}
	if _, err := n.ServeDNS(context.Background(), w, query("missing."+zone, dns.TypeA, true)); err != nil {
		t.Fatal(err)
	}
	after := testutil.ToFloat64(denialsIssued.WithLabelValues(server, "nxdomain"))
	if after <= before {
		t.Errorf("denials_total{type=nxdomain} didn't advance: %f → %f", before, after)
	}
}

func TestChainEntriesGaugeSet(t *testing.T) {
	n := &Nsec3Sign{
		Salt:     "aabbccdd",
		ZoneFile: writeZone(t, flatZone),
	}
	if err := n.loadChain(); err != nil {
		t.Fatal(err)
	}
	got := testutil.ToFloat64(chainEntries.WithLabelValues("example.test."))
	if got <= 0 {
		t.Fatalf("chain_entries{zone=example.test.} = %f, want > 0", got)
	}
	// flatZone has apex + ns1 + host + secure + insecure = 5 names.
	if want := float64(5); got != want {
		t.Errorf("chain_entries = %f, want %f", got, want)
	}
}
