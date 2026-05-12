// Denial-of-existence proofs.
//
// When the data-source plugin returns NXDOMAIN or NODATA, validators
// need NSEC3 records in the authority section to be willing to
// believe the denial. This file owns the proof-construction logic
// per RFC 5155 §7:
//
//   - NXDOMAIN: a *closest-encloser proof* — matching NSEC3 for the
//     longest existing ancestor, covering NSEC3 for the "next closer"
//     name one label below, covering NSEC3 for `*.<encloser>`.
//
//   - NODATA: a *matching NSEC3 for QNAME* whose type bitmap
//     unambiguously lacks the queried type.
//
// `attachDenialProof` is the ServeDNS-side entry point: it classifies
// the response via CoreDNS's `response.Typify`, picks the right
// proof generator, and appends the NSEC3 RRs to the authority
// section. signMessage (step 4) then signs every RRset in that
// section, including the new NSEC3s, so the denial ships fully
// signed.
//
// Wildcard-expansion proofs (RFC 5155 §7.2.5) and delegation /
// referral proofs are deferred to step 5b alongside the file-plugin
// integration that actually populates Nsec3Sign.Chain in production.
// In the meantime tests inject a chain directly.

package nsec3sign

import (
	"strings"
	"time"

	"github.com/coredns/coredns/plugin/pkg/response"

	"github.com/miekg/dns"
)

// defaultDenialTTL is the fallback negative-cache TTL when the
// response's authority section doesn't carry a SOA we can read the
// minimum off. 3600 s matches what most operators use; the real
// value comes from the SOA Minttl field per RFC 2308 §3 when
// available.
const defaultDenialTTL uint32 = 3600

// attachDenialProof inspects the downstream response and, if it's
// an NXDOMAIN or NODATA inside the configured zone, appends the
// matching NSEC3 records to the authority section. The mutation
// happens before signMessage walks Ns, so the new NSEC3 RRsets pick
// up RRSIGs automatically — the denial ships fully signed.
//
// No-op when the chain hasn't been populated yet (the file-plugin
// integration that does that lands in step 5b). That's by design:
// step 5 ships the algorithm + tests with manual chain injection,
// so production wiring can land independently without holding up
// the algorithm review.
func (n *Nsec3Sign) attachDenialProof(m *dns.Msg, qname string, now time.Time) *dns.Msg {
	if n.Chain == nil || len(n.Chain.nodes) == 0 {
		return m
	}
	ttl := readDenialTTL(m)
	rt, _ := response.Typify(m, now)
	switch rt {
	case response.NameError:
		m.Ns = append(m.Ns, n.Chain.proofForNXDOMAIN(qname, ttl)...)
	case response.NoData:
		m.Ns = append(m.Ns, n.Chain.proofForNODATA(qname, ttl)...)
	}
	return m
}

// readDenialTTL pulls the SOA Minttl out of the authority section,
// matching how validators expect to cache the denial. Falls back to
// defaultDenialTTL when no SOA is present — that path mostly hits
// in degenerate responses from misconfigured backends.
func readDenialTTL(m *dns.Msg) uint32 {
	for _, rr := range m.Ns {
		if soa, ok := rr.(*dns.SOA); ok {
			return soa.Minttl
		}
	}
	return defaultDenialTTL
}

// proofForNXDOMAIN returns the closest-encloser proof for qname per
// RFC 5155 §7.2.1: matching NSEC3 for the encloser, covering NSEC3
// for the next-closer name, covering NSEC3 for the wildcard at the
// encloser. Up to three records; deduped when two of those lookups
// collapse to the same chain node.
func (c *chain) proofForNXDOMAIN(qname string, ttl uint32) []dns.RR {
	if len(c.nodes) == 0 {
		return nil
	}
	encloser, nextCloser := c.findClosestEncloser(qname)
	wildcard := "*." + encloser

	matchEncl := c.matchingNSEC3(encloser)
	coverNext := c.coveringNSEC3(nextCloser)
	coverWild := c.coveringNSEC3(wildcard)

	return c.dedupedNSEC3([]*nsec3Node{matchEncl, coverNext, coverWild}, ttl)
}

// proofForNODATA returns the matching NSEC3 for qname per RFC 5155
// §7.2.3 — proves the name exists but doesn't have the queried
// type. The type bitmap on the NSEC3 record (taken from the chain
// node's recorded Types) is what the validator inspects.
//
// When qname doesn't actually have a matching node — the data
// source called NODATA on a non-existent name — we fall back to the
// full NXDOMAIN proof so the validator doesn't reject the response.
// Better to over-prove than to ship a denial validators won't trust.
func (c *chain) proofForNODATA(qname string, ttl uint32) []dns.RR {
	if len(c.nodes) == 0 {
		return nil
	}
	if node := c.matchingNSEC3(qname); node != nil {
		return c.dedupedNSEC3([]*nsec3Node{node}, ttl)
	}
	return c.proofForNXDOMAIN(qname, ttl)
}

// dedupedNSEC3 renders an ordered list of chain nodes into NSEC3 RRs,
// skipping nils and collapsing duplicate hashes. The order is
// preserved for the records that survive — RFC 5155 doesn't mandate
// an order, but ordering by proof role (encloser, next-closer,
// wildcard) makes packet captures readable.
func (c *chain) dedupedNSEC3(nodes []*nsec3Node, ttl uint32) []dns.RR {
	seen := make(map[string]struct{}, len(nodes))
	out := make([]dns.RR, 0, len(nodes))
	for _, n := range nodes {
		if n == nil {
			continue
		}
		if _, dup := seen[n.Hash]; dup {
			continue
		}
		seen[n.Hash] = struct{}{}
		out = append(out, c.renderNSEC3(n, ttl))
	}
	return out
}

// renderNSEC3 turns an internal chain node into a wire-shaped NSEC3
// RR. The owner name is `<base32hex>.<apex>`; the next-hashed-owner
// field comes from the chain's circular successor; the type bitmap
// is the node's recorded types. Flags encode the opt-out bit when
// the chain is opted out.
func (c *chain) renderNSEC3(n *nsec3Node, ttl uint32) *dns.NSEC3 {
	var flags uint8
	if c.OptOut {
		flags = 1 // RFC 5155 §3.1.2 — opt-out flag
	}
	// SaltLength is the BYTE length of the salt — c.Salt is the
	// hex-encoded form, so length divides by two. miekg/dns expects
	// the Salt field to stay hex-encoded; it converts on the wire.
	return &dns.NSEC3{
		Hdr: dns.RR_Header{
			Name:   c.ownerForNode(n),
			Rrtype: dns.TypeNSEC3,
			Class:  dns.ClassINET,
			Ttl:    ttl,
		},
		Hash:       nsec3HashSHA1,
		Flags:      flags,
		Iterations: c.Iterations,
		SaltLength: uint8(len(c.Salt) / 2),
		Salt:       c.Salt,
		HashLength: 20, // SHA-1 is always 20 bytes
		NextDomain: c.nextHashedOwner(n),
		TypeBitMap: append([]uint16(nil), n.Types...),
	}
}

// findClosestEncloser walks qname's ancestor labels and returns the
// longest one that has a matching NSEC3 in the chain (the *closest
// encloser*), plus the "next closer" — the name exactly one label
// longer on the path from encloser to qname.
//
// The fallback when nothing matches is the zone apex paired with a
// next-closer one label deeper than the apex; the apex always
// exists conceptually even when its NSEC3 isn't materialized in
// the chain (e.g. tests with abbreviated input).
func (c *chain) findClosestEncloser(qname string) (encloser, nextCloser string) {
	qname = dns.CanonicalName(qname)
	qlabels := dns.SplitDomainName(qname)
	apexLabels := dns.SplitDomainName(c.Apex)

	// Drop labels off the left of qname one at a time until we hit
	// either a matching ancestor or the apex. The match-at-i case
	// makes labels[i-1:] the next closer; the all-the-way-up case
	// hands back the apex and the deepest label below it.
	for i := 0; i <= len(qlabels)-len(apexLabels); i++ {
		candidate := joinDomainLabels(qlabels[i:])
		if c.matchingNSEC3(candidate) != nil {
			if i == 0 {
				// qname itself matched — caller should be on the
				// NODATA path. Returning qname-as-encloser keeps
				// the function total; the NXDOMAIN proof callers
				// route through here only when matchingNSEC3
				// already failed on qname.
				return candidate, qname
			}
			return candidate, joinDomainLabels(qlabels[i-1:])
		}
	}
	// Walked all the way without a match. The encloser is the apex;
	// the next closer is one label deeper than the apex. If qname
	// is shorter than the apex (malformed input) the second slice
	// would underflow, so guard with a length check.
	if len(qlabels) > len(apexLabels) {
		return c.Apex, joinDomainLabels(qlabels[len(qlabels)-len(apexLabels)-1:])
	}
	return c.Apex, qname
}

// joinDomainLabels glues a label slice back into a fully-qualified
// name with trailing dot. The empty slice → "." (the root).
func joinDomainLabels(labels []string) string {
	if len(labels) == 0 {
		return "."
	}
	return strings.Join(labels, ".") + "."
}
