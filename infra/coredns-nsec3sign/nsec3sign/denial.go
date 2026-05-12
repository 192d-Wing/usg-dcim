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
// Known gaps documented in SECURITY-REVIEW.md (correctness, not
// security): wildcard-expansion proofs (RFC 5155 §7.2.5) aren't
// synthesized; delegation referrals don't get NSEC3 DS-attestation
// proofs. Both affect zones that use those features — flat DCIM
// host zones (the design target) don't trigger them.

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
// `server` is the request's server-block label, used as the metric
// dimension for the denials counter. No-op when the chain hasn't
// been populated (no `zone file` directive) — the cache + signing
// path keeps working, just without denial proofs. Validators will
// reject denial responses in that mode; the empty-keys WARNING at
// startup mirrors the same intent: silent unsigned shipping is not
// a default we sleep-walk into.
func (n *Nsec3Sign) attachDenialProof(m *dns.Msg, qname, server string, now time.Time) *dns.Msg {
	if n.Chain == nil || len(n.Chain.nodes) == 0 {
		return m
	}
	ttl := readDenialTTL(m)
	rt, _ := response.Typify(m, now)
	switch rt {
	case response.NameError:
		m.Ns = append(m.Ns, n.Chain.proofForNXDOMAIN(qname, ttl)...)
		denialsIssued.WithLabelValues(server, "nxdomain").Inc()
	case response.NoData:
		m.Ns = append(m.Ns, n.Chain.proofForNODATA(qname, ttl)...)
		denialsIssued.WithLabelValues(server, "nodata").Inc()
	case response.Delegation:
		// Referral to a child zone — the validator needs to know
		// whether the delegation is secure (DS present, follow into
		// the child) or insecure (no DS, child stays unsigned).
		if delOwner := delegationOwner(m.Ns); delOwner != "" {
			m.Ns = append(m.Ns, n.Chain.proofForDelegation(delOwner, ttl)...)
			denialsIssued.WithLabelValues(server, "delegation").Inc()
		}
	}
	return m
}

// delegationOwner returns the owner name of the NS RRset in the
// authority section — that's the delegation point per RFC 1034.
// Empty when there's no NS record (defensive; response.Typify
// would not have returned Delegation in that case).
func delegationOwner(ns []dns.RR) string {
	for _, rr := range ns {
		if rr.Header().Rrtype == dns.TypeNS {
			return dns.CanonicalName(rr.Header().Name)
		}
	}
	return ""
}

// attachWildcardProof scans the answer section for RRsets that came
// from a wildcard expansion (owner isn't in the chain, but a
// sibling `*.<closest-encloser>` IS) and appends the §7.2.4
// covering NSEC3 for the "next closer" name to the authority
// section. Validators see the wildcard-shaped RRSIG.Labels in the
// answer, look for the covering NSEC3, and use it to confirm that
// the queried name doesn't exist as a concrete owner — proving
// the wildcard match was legitimate.
//
// No-op when the chain isn't populated, when the answer is empty,
// or when none of the answer RRsets came from a wildcard. Multiple
// answer RRsets with different owners (rare) get one covering
// NSEC3 per distinct next-closer, deduped.
func (n *Nsec3Sign) attachWildcardProof(m *dns.Msg, server string) *dns.Msg {
	if n.Chain == nil || len(n.Chain.nodes) == 0 || len(m.Answer) == 0 {
		return m
	}
	ttl := readDenialTTL(m)
	seenOwner := make(map[string]struct{})
	covered := make(map[string]struct{})
	var emitted bool
	for _, rr := range m.Answer {
		h := rr.Header()
		if h.Rrtype == dns.TypeRRSIG {
			continue
		}
		owner := dns.CanonicalName(h.Name)
		if _, dup := seenOwner[owner]; dup {
			continue
		}
		seenOwner[owner] = struct{}{}
		if n.Chain.wildcardSource(owner) == "" {
			continue
		}
		// Wildcard expansion confirmed — emit the covering NSEC3
		// for the next-closer name. nextCloser equals `owner` here
		// because findClosestEncloser walks up from `owner` and
		// nextCloser is one label longer than the encloser on the
		// path back to qname — i.e. qname itself.
		_, nextCloser := n.Chain.findClosestEncloser(owner)
		cover := n.Chain.coveringNSEC3(nextCloser)
		if cover == nil {
			continue
		}
		if _, dup := covered[cover.Hash]; dup {
			continue
		}
		covered[cover.Hash] = struct{}{}
		m.Ns = append(m.Ns, n.Chain.renderNSEC3(cover, ttl))
		emitted = true
	}
	if emitted {
		denialsIssued.WithLabelValues(server, "wildcard").Inc()
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

// proofForDelegation returns the NSEC3 record(s) that prove a
// delegation's security status per RFC 5155 §7.2.7 — used on
// referral responses (NS+DS or NS-only in the authority section).
//
// Three cases:
//
//   - Delegation owner is in the chain: return the matching NSEC3.
//     Its type bitmap shows `NS` (always) plus `DS` when the
//     delegation is secure. Validators read the bitmap to decide
//     whether to chase a DS into the child zone.
//
//   - Owner not in the chain, opt-out is enabled: the delegation
//     was elided at chain-build time (RFC 5155 §6). Return the
//     covering NSEC3 — its opt-out flag and the parent's chain
//     position together prove the gap contains no DS.
//
//   - Owner not in the chain, opt-out is disabled: shouldn't
//     happen for a well-formed zone, but return nil rather than
//     fabricate a proof.
func (c *chain) proofForDelegation(delOwner string, ttl uint32) []dns.RR {
	if len(c.nodes) == 0 {
		return nil
	}
	if node := c.matchingNSEC3(delOwner); node != nil {
		return c.dedupedNSEC3([]*nsec3Node{node}, ttl)
	}
	if c.OptOut {
		if node := c.coveringNSEC3(delOwner); node != nil {
			return c.dedupedNSEC3([]*nsec3Node{node}, ttl)
		}
	}
	return nil
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
