// RRSIG cache.
//
// Online signing is dominated by the ECDSA / RSA math — typically
// 50–500 µs per RRSIG depending on algorithm and key size. Caching
// the result keyed by the RRset's contents collapses that cost to
// near-zero on repeat queries, which is the common case for
// authoritative pods.
//
// We mirror the upstream `dnssec` plugin's design: hash the RRset's
// presentation form for the cache key (so a content change forces a
// resign), evict the LRU when full, and run a periodic janitor that
// drops cached signatures past 75% of their validity window so the
// next query touching that RRset re-signs with a fresh inception.

package nsec3sign

import (
	"hash/fnv"
	"io"
	"time"

	"github.com/coredns/coredns/plugin/pkg/cache"

	"github.com/miekg/dns"
)

// sigCacheRefreshAt sets the renewal point — RRSIGs past this
// fraction of their 8-day validity are dropped on the next janitor
// pass and resigned on the next query. Matches upstream's "2 days
// before expiration" rule (8d - 2d = 75% mark).
const sigCacheRefreshThreshold = 2 * 24 * time.Hour

// sigCacheCleanInterval is how often the janitor wakes up. Eight
// hours is generous — RRSIGs are valid 8 days, so even sleeping a
// whole day wouldn't risk shipping expired sigs. Matching upstream
// keeps the goroutine count predictable in shared monitoring.
const sigCacheCleanInterval = 8 * time.Hour

// rrsetCacheKey hashes an RRset into the cache key. We use the
// presentation-form `rr.String()` so a content change (zone reload,
// DDNS update) forces a fresh signature rather than serving a stale
// one. fnv-64a is fast and good-enough for collision avoidance on
// the typical zone size (thousands of RRsets).
func rrsetCacheKey(rrs []dns.RR) uint64 {
	h := fnv.New64a()
	for _, rr := range rrs {
		io.WriteString(h, rr.String())
	}
	return h.Sum64()
}

// runSigCacheJanitor walks the cache periodically and drops any
// entry whose RRSIGs are within `sigCacheRefreshThreshold` of
// expiring. The next query touching that RRset will sign fresh.
//
// Returns immediately if `c` or `stop` is nil — defensive against
// the test path that constructs Nsec3Sign without a real cache.
func runSigCacheJanitor(c *cache.Cache[[]dns.RR], stop <-chan struct{}) {
	if c == nil || stop == nil {
		return
	}
	tick := time.NewTicker(sigCacheCleanInterval)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			cleanSigCache(c, time.Now().UTC())
		case <-stop:
			return
		}
	}
}

// cleanSigCache evicts entries whose RRSIGs are within the refresh
// threshold of expiring. Exported (lowercase, same package) so the
// test suite can drive eviction deterministically without waiting
// for the janitor tick.
func cleanSigCache(c *cache.Cache[[]dns.RR], now time.Time) {
	threshold := now.Add(sigCacheRefreshThreshold)
	c.Walk(func(items map[uint64][]dns.RR, key uint64) bool {
		for _, rr := range items[key] {
			sig, ok := rr.(*dns.RRSIG)
			if !ok {
				continue
			}
			// ValidityPeriod returns true when `now` falls inside
			// the RRSIG's inception–expiration window. Inverting
			// gives us "this sig is too close to (or past)
			// expiration to keep using."
			if !sig.ValidityPeriod(threshold) {
				delete(items, key)
				return true
			}
		}
		return true
	})
}
