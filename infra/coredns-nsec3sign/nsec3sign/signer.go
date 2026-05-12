// RRSIG generation.
//
// `signMessage` is the top-level entry point: given a response from
// the data-source plugin, it groups records into RRsets and attaches
// one RRSIG per applicable signing key. Canonical RRset ordering and
// wire encoding (RFC 4034 §6) are delegated to miekg/dns's
// `RRSIG.Sign`; we just fill the template and pick the right keys.
//
// signRRset routes through the LRU signature cache in sigcache.go
// before doing the math — on hit the same []dns.RR slice is reused,
// so callers MUST treat the returned RRSIGs as immutable. The current
// call path (appendRRSIGs → serialized WriteMsg) doesn't mutate, but
// future maintainers should keep that invariant.

package nsec3sign

import (
	"crypto"
	"fmt"
	"strings"
	"time"

	"github.com/miekg/dns"
)

const (
	// sigInceptionOffset is the lookback for the RRSIG inception
	// time. Validators with a slightly fast clock would reject a
	// "signed-in-the-future" RRSIG, so we backdate by an hour.
	sigInceptionOffset = 1 * time.Hour

	// sigValidity is how long an RRSIG is valid from inception.
	// Eight days matches CoreDNS's upstream `dnssec` plugin and
	// gives validators plenty of slack if the auth pod is offline
	// for a few days running.
	sigValidity = 8 * 24 * time.Hour
)

// rrsetKey is the (name, type, class) triple that identifies one
// RRset. Name is lowercased for grouping; the signature itself uses
// whatever casing the data-source plugin emitted.
type rrsetKey struct {
	name   string
	rrtype uint16
	class  uint16
}

// signMessage attaches RRSIGs to every RRset in the answer and
// authority sections of m. The Additional section is intentionally
// not signed — RFC 4035 §3.1.4 makes that optional, and the records
// typically found there (glue, OPT) don't gain much from a signature
// that doubles the response size.
//
// `server` is the request's server-block label (used as the metric
// dimension for cache hits / misses). Returns m for call-chain
// convenience. With no keys loaded, m is returned unchanged — the
// no-op path is the responsibility of ServeDNS, which short-circuits
// before calling here, but the guard is cheap and prevents an
// empty-loop reallocation.
func (n *Nsec3Sign) signMessage(m *dns.Msg, signerName, server string, now time.Time) *dns.Msg {
	if len(n.Keys) == 0 {
		return m
	}
	incep := uint32(now.Add(-sigInceptionOffset).Unix())
	expir := uint32(now.Add(sigValidity).Unix())

	m.Answer = n.appendRRSIGs(m.Answer, signerName, server, incep, expir)
	m.Ns = n.appendRRSIGs(m.Ns, signerName, server, incep, expir)
	return m
}

// appendRRSIGs groups section by RRset and appends one RRSIG per
// applicable signing key. The map iteration happens against a
// snapshot built from the input slice, so the in-place append
// doesn't invalidate the iteration.
func (n *Nsec3Sign) appendRRSIGs(section []dns.RR, signerName, server string, incep, expir uint32) []dns.RR {
	for _, rrset := range groupByRRset(section) {
		section = append(section, n.signRRset(rrset, signerName, server, incep, expir)...)
	}
	return section
}

// signRRset returns RRSIGs over rrs, one per key applicable to the
// RRset's type. Cache-aware: on hit, the previously-signed RRSIGs
// are returned without re-running the signature math (which costs
// 50–500 µs per RR depending on algorithm). Individual key errors
// are logged at warning level and the offending key skipped — the
// remaining signatures still ship, which is preferable to dropping
// a positive response entirely on one key's failure.
func (n *Nsec3Sign) signRRset(rrs []dns.RR, signerName, server string, incep, expir uint32) []dns.RR {
	if len(rrs) == 0 {
		return nil
	}

	// Cache lookup: the key hashes the RRset's presentation form, so
	// a content change (zone reload, DDNS update) misses naturally
	// and forces a re-sign. Stale entries past 75 % of validity get
	// evicted by the janitor goroutine.
	var cacheKey uint64
	if n.SigCache != nil {
		cacheKey = rrsetCacheKey(rrs)
		if v, ok := n.SigCache.Get(cacheKey); ok {
			cacheHits.WithLabelValues(server).Inc()
			return v.([]dns.RR)
		}
		cacheMisses.WithLabelValues(server).Inc()
	}

	keys := n.signingKeysFor(rrs[0].Header().Rrtype)
	if len(keys) == 0 {
		return nil
	}
	// Wildcard handling: if the answer came from a wildcard
	// expansion, sign over the WILDCARD owner so miekg/dns produces
	// the right canonical form + Labels field per RFC 4034 §3.1.3.
	// `signRRs` becomes a clone of `rrs` with each header's Name
	// rewritten; the original qname is restored on the resulting
	// RRSIG below. (miekg/dns's `RRSIG.Sign` overwrites Labels
	// from the RRset's owner name regardless of any pre-set value
	// — so we can't just compute Labels separately.)
	signRRs := rrs
	var qnameOwner string
	if n.Chain != nil {
		if wcSource := n.Chain.wildcardSource(rrs[0].Header().Name); wcSource != "" {
			qnameOwner = rrs[0].Header().Name
			signRRs = wildcardOwnerClone(rrs, wcSource)
		}
	}
	sigs := make([]dns.RR, 0, len(keys))
	for _, k := range keys {
		sig, err := signOne(signRRs, k, signerName, incep, expir)
		if err != nil {
			log.Warningf("sign %s/%s with keytag %d: %v",
				rrs[0].Header().Name,
				dns.TypeToString[rrs[0].Header().Rrtype],
				k.KeyTag, err)
			continue
		}
		if qnameOwner != "" {
			// Restore the queried name as the RRSIG owner. Labels is
			// already correct (miekg/dns set it from the wildcard
			// owner during Sign); only Hdr.Name needs the patch so
			// the RR ships out with the same owner as the answer
			// RRset it covers.
			sig.Hdr.Name = qnameOwner
		}
		sigs = append(sigs, sig)
	}

	if n.SigCache != nil && len(sigs) > 0 {
		n.SigCache.Add(cacheKey, sigs)
		cacheEntries.WithLabelValues(server, "signature").Set(float64(n.SigCache.Len()))
	}
	return sigs
}

// signingKeysFor returns the keys that should sign an RRset of the
// given type. DNSKEY gets signed by KSKs (the chain validators
// follow to the parent DS); everything else by ZSKs. When only one
// role exists we fall back to whatever's available so an operator
// running a single combined-signing key still gets coverage.
func (n *Nsec3Sign) signingKeysFor(rrtype uint16) []*signingKey {
	if rrtype == dns.TypeDNSKEY {
		if ks := n.KSKs(); len(ks) > 0 {
			return ks
		}
		return n.ZSKs()
	}
	if zs := n.ZSKs(); len(zs) > 0 {
		return zs
	}
	return n.KSKs()
}

// signOne fills an RRSIG template from rrs[0]'s header + key
// metadata and runs the actual signature via miekg/dns. The library
// handles canonical RRset ordering and wire encoding per RFC 4034
// §6, so we never have to think about byte-level details here.
//
// The Labels field is set by miekg/dns from the RRset's owner name
// — it counts labels and subtracts one for a `*.` prefix per RFC
// 4034 §3.1.3. Callers that need wildcard-aware Labels rewrite the
// RRset's owner to the wildcard form before calling here (see
// signRRset's wildcard branch).
func signOne(rrs []dns.RR, k *signingKey, signerName string, incep, expir uint32) (*dns.RRSIG, error) {
	rh := rrs[0].Header()
	sig := &dns.RRSIG{
		Hdr: dns.RR_Header{
			Name:   rh.Name,
			Rrtype: dns.TypeRRSIG,
			Class:  rh.Class,
			Ttl:    rh.Ttl,
		},
		TypeCovered: rh.Rrtype,
		Algorithm:   k.Public.Algorithm,
		OrigTtl:     rh.Ttl,
		Expiration:  expir,
		Inception:   incep,
		KeyTag:      k.KeyTag,
		SignerName:  signerName,
	}
	signer, ok := k.Private.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("private key for keytag %d does not implement crypto.Signer", k.KeyTag)
	}
	if err := sig.Sign(signer, rrs); err != nil {
		return nil, fmt.Errorf("RRSIG.Sign: %w", err)
	}
	return sig, nil
}

// wildcardOwnerClone returns a deep-enough copy of `rrs` with each
// RR's owner-name header rewritten to `wildcardOwner`. The copies
// share rdata (we don't mutate that), but miekg/dns canonical
// encoding pulls owner names from the header, so this is enough to
// drive the signer down the wildcard path.
func wildcardOwnerClone(rrs []dns.RR, wildcardOwner string) []dns.RR {
	out := make([]dns.RR, len(rrs))
	for i, rr := range rrs {
		c := dns.Copy(rr)
		c.Header().Name = wildcardOwner
		out[i] = c
	}
	return out
}

// rrsigLabels computes the RRSIG Labels field per RFC 4034 §3.1.3:
// "the number of labels in the RRSIG owner name, minus one if the
// owner name contains a wildcard label." For typical concrete owner
// names this is just CountLabel; for wildcard expansions the data-
// source plugin rewrites the owner to the queried name, so the
// wildcard branch is rare in positive responses but cheap to keep.
func rrsigLabels(name string) uint8 {
	n := dns.CountLabel(name)
	if strings.HasPrefix(name, "*.") {
		n--
	}
	return uint8(n)
}

// groupByRRset bins records by (name, type, class). RRSIGs and OPT
// records are skipped: we never sign an existing signature, and OPT
// is EDNS metadata that lives outside the data plane.
func groupByRRset(rrs []dns.RR) map[rrsetKey][]dns.RR {
	out := make(map[rrsetKey][]dns.RR)
	for _, rr := range rrs {
		h := rr.Header()
		if h.Rrtype == dns.TypeRRSIG || h.Rrtype == dns.TypeOPT {
			continue
		}
		k := rrsetKey{
			name:   strings.ToLower(h.Name),
			rrtype: h.Rrtype,
			class:  h.Class,
		}
		out[k] = append(out[k], rr)
	}
	return out
}
