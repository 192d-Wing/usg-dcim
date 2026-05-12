// Signature-cache tests.
//
// We drive the cache through the real signRRset path rather than
// poking the cache directly — the contract that matters is "same
// RRset → no resign", and verifying that end-to-end catches a wider
// class of regression than a direct .Get/.Add test would.

package nsec3sign

import (
	"context"
	"testing"
	"time"

	"github.com/coredns/coredns/plugin/pkg/cache"

	"github.com/miekg/dns"
)

func TestSigCacheHitOnRepeatRRset(t *testing.T) {
	const zone = "example.test."
	a := &dns.A{
		Hdr: dns.RR_Header{Name: "host." + zone, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
		A:   []byte{10, 0, 0, 1},
	}
	n, _ := newPluginWithKey(t, zone, 256, []dns.RR{a}, nil)
	n.SigCache = cache.New[[]dns.RR](64)

	// First call: cache miss, computes a signature.
	w1 := &captureWriter{}
	if _, err := n.ServeDNS(context.Background(), w1, query("host."+zone, dns.TypeA, true)); err != nil {
		t.Fatal(err)
	}
	sig1 := findRRSIG(w1.captured.Answer, dns.TypeA)
	if sig1 == nil {
		t.Fatal("no RRSIG on first call")
	}

	// Second call with the same RRset: cache hit. The returned RRSIG
	// is the same byte-for-byte object the cache stored. Equality on
	// the .Signature field is the strongest check we can do without
	// knowing the cache key — same inception, same signature bytes,
	// same expiration mean no re-sign happened.
	w2 := &captureWriter{}
	if _, err := n.ServeDNS(context.Background(), w2, query("host."+zone, dns.TypeA, true)); err != nil {
		t.Fatal(err)
	}
	sig2 := findRRSIG(w2.captured.Answer, dns.TypeA)
	if sig2 == nil {
		t.Fatal("no RRSIG on second call")
	}
	if sig1.Signature != sig2.Signature {
		t.Fatal("signature bytes differ between calls — cache didn't hit")
	}
	if sig1.Inception != sig2.Inception {
		t.Fatal("inception differs — cache returned a freshly-signed RRSIG")
	}
}

func TestSigCacheMissOnDifferentRRset(t *testing.T) {
	const zone = "example.test."
	host := &dns.A{
		Hdr: dns.RR_Header{Name: "host." + zone, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
		A:   []byte{10, 0, 0, 1},
	}
	other := &dns.A{
		Hdr: dns.RR_Header{Name: "other." + zone, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
		A:   []byte{10, 0, 0, 2},
	}
	// stub returns whichever matches the query. The simplest stub
	// just returns both records but only one will be relevant per
	// query; the cache key includes owner+content so each gets its
	// own slot.
	n, _ := newPluginWithKey(t, zone, 256, []dns.RR{host, other}, nil)
	n.SigCache = cache.New[[]dns.RR](64)

	for _, qname := range []string{"host." + zone, "other." + zone} {
		w := &captureWriter{}
		if _, err := n.ServeDNS(context.Background(), w, query(qname, dns.TypeA, true)); err != nil {
			t.Fatalf("%s: %v", qname, err)
		}
	}
	// Both owners should land in the cache; 2 distinct entries
	// would be visible via Len(). The stub returns BOTH records on
	// both queries, so we'd actually see 2 cached entries — one per
	// (owner, A, IN) RRset.
	if got := n.SigCache.Len(); got < 2 {
		t.Fatalf("cache has %d entries; expected at least 2 distinct RRsets", got)
	}
}

func TestSigCacheCleanEvictsExpiringEntries(t *testing.T) {
	c := cache.New[[]dns.RR](16)

	// Plant two entries: one with a healthy validity window, one
	// already inside the refresh threshold. cleanSigCache should
	// drop the expiring one and keep the fresh one.
	now := time.Now().UTC()
	fresh := &dns.RRSIG{
		Inception:  uint32(now.Add(-1 * time.Hour).Unix()),
		Expiration: uint32(now.Add(7 * 24 * time.Hour).Unix()),
	}
	expiring := &dns.RRSIG{
		Inception:  uint32(now.Add(-7 * 24 * time.Hour).Unix()),
		Expiration: uint32(now.Add(1 * time.Hour).Unix()), // well inside the 2-day refresh window
	}
	c.Add(1, []dns.RR{fresh})
	c.Add(2, []dns.RR{expiring})

	cleanSigCache(c, now)

	if _, ok := c.Get(1); !ok {
		t.Error("fresh sig was evicted")
	}
	if _, ok := c.Get(2); ok {
		t.Error("expiring sig was NOT evicted")
	}
}

func TestSigCacheJanitorRespectsStop(t *testing.T) {
	// Janitor must terminate promptly when the stop channel closes
	// — otherwise plugin reloads would leak goroutines across
	// restarts.
	c := cache.New[[]dns.RR](8)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		runSigCacheJanitor(c, stop)
		close(done)
	}()
	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("janitor did not exit within 2 s of stop being closed")
	}
}

func TestRrsetCacheKeyContentSensitive(t *testing.T) {
	// Identical RRsets must hash identically; a content change must
	// shift the hash so the next sign-path is a miss. This is the
	// guarantee the upstream `dnssec` plugin makes — we match it so
	// a zone-file edit re-signs naturally without needing a manual
	// cache flush.
	a1 := &dns.A{
		Hdr: dns.RR_Header{Name: "host.example.test.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
		A:   []byte{10, 0, 0, 1},
	}
	a2 := &dns.A{
		Hdr: dns.RR_Header{Name: "host.example.test.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
		A:   []byte{10, 0, 0, 1},
	}
	a3 := &dns.A{
		Hdr: dns.RR_Header{Name: "host.example.test.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
		A:   []byte{10, 0, 0, 2}, // content change
	}
	if rrsetCacheKey([]dns.RR{a1}) != rrsetCacheKey([]dns.RR{a2}) {
		t.Fatal("identical RRsets must hash identically")
	}
	if rrsetCacheKey([]dns.RR{a1}) == rrsetCacheKey([]dns.RR{a3}) {
		t.Fatal("content change must shift the cache hash")
	}
}
