// NSEC3 chain builder.
//
// For NSEC3-signed zones, every owner name in the zone is hashed
// (RFC 5155 §5: iterated salted SHA-1, base32hex-encoded) and the
// hashes form a sorted, circular linked list. Denial of existence
// (NXDOMAIN / NODATA) is proved by returning the NSEC3 records that
// *cover* the queried name's hash — the ones whose hash range
// brackets it.
//
// We pre-compute the full chain at zone-load time, sorted by hash,
// so each query-time denial is an O(log n) binary search. Lazy
// hashing isn't viable here: finding the covering record requires
// the *neighbouring* hash, which you can only know by walking the
// full set.
//
// This file owns the chain construction + lookup primitives. The
// machinery that turns a covering / matching node into actual
// `*dns.NSEC3` resource records lives in denial.go; zone-data
// ingestion that produces the `nameInfo` slice lives in zone.go.

package nsec3sign

import (
	"sort"
	"strings"

	"github.com/miekg/dns"
)

// nsec3HashSHA1 is the only NSEC3 hash algorithm currently defined
// by IANA (RFC 5155 §11.1). Stashed as a named constant so the
// intent reads clearly at call sites — the literal `1` would look
// like a magic number sitting next to iteration counts.
const nsec3HashSHA1 uint8 = 1

// nsec3Hash wraps `miekg/dns.HashName` and forces lowercase output.
// miekg/dns returns uppercase base32hex, but RFC 5155 examples, dig,
// and every operator-facing tool we care about use lowercase — and
// internal consistency keeps the string-equality comparisons in
// matchingNSEC3 / coveringNSEC3 honest no matter where the input
// came from (chain build vs. query-time QNAME hash).
func nsec3Hash(name, salt string, iterations uint16) string {
	return strings.ToLower(dns.HashName(name, nsec3HashSHA1, iterations, salt))
}

// nsec3Node is one entry in the precomputed chain. Each node maps a
// zone owner name to its NSEC3 hash; the slice in `chain.nodes` is
// sorted by Hash so we can binary-search.
type nsec3Node struct {
	// Hash is the base32hex-encoded NSEC3 hash of OwnerName. It's
	// the lookup key and also the leftmost label of the NSEC3 RR's
	// owner name in the served zone.
	Hash string

	// OwnerName is the original owner name (canonical form, lower-
	// case, trailing dot). Kept for log/metrics readability and for
	// the type-bitmap RFC 5155 §3.2 emission in denial.go.
	OwnerName string

	// Types is the sorted set of RR types present at OwnerName. The
	// denial path copies this into the NSEC3 RR's type bitmap. nil
	// is valid — it corresponds to an "empty non-terminal" name that
	// exists only because something below it does.
	Types []uint16

	// OptedOut marks an insecure delegation that the chain builder
	// decided to elide (RFC 5155 §6). When the zone's opt-out flag
	// is set, opted-out names are dropped from the chain entirely
	// rather than kept with this flag — so today this field is only
	// meaningful for callers that want to introspect the input. It's
	// kept on the struct for symmetry with `nameInfo`.
	OptedOut bool
}

// chain is the immutable, sorted NSEC3 chain for one signed zone.
// Built once at zone load and rebuilt on reload; lookups are O(log n).
type chain struct {
	// Apex is the zone's apex (canonical form, trailing dot).
	// Stored so denial.go can synthesize NSEC3 owner names —
	// they live at <base32hex-hash>.<apex>.
	Apex string

	// Salt + Iterations + OptOut are the NSEC3PARAM parameters used
	// to build the chain. Carried here so a future incremental update
	// (DDNS) can detect a parameter change and force a full rebuild
	// rather than mixing hashes from two parameter sets.
	Salt       string
	Iterations uint16
	OptOut     bool

	// nodes is sorted by Hash ascending. Callers go through
	// matchingNSEC3 / coveringNSEC3 rather than touching this slice
	// directly so the binary search stays in one place.
	nodes []nsec3Node
}

// nameInfo is one input to buildChain: an owner name from the zone
// plus the types present there. Producing this slice is the caller's
// job — the eventual file-plugin integration will walk the zone
// tree, collapse duplicates, expand empty non-terminals, and filter
// out occluded names before handing them here.
type nameInfo struct {
	// Name is presentation form; the chain builder canonicalizes.
	Name string

	// Types lists the RR types present at Name. Order doesn't matter
	// — buildChain sorts ascending for the bitmap.
	Types []uint16

	// OptedOut should be true only for insecure-delegation NS names
	// (no DS at the same owner) when the zone is in opt-out mode.
	OptedOut bool
}

// buildChain hashes every input name, sorts by hash, and returns the
// resulting chain. O(n log n) in zone size at build, O(log n) per
// lookup. Returns a non-nil *chain even when names is empty so the
// caller doesn't need a nil-check before lookup.
func buildChain(apex, salt string, iterations uint16, optOut bool, names []nameInfo) *chain {
	apexCanon := dns.CanonicalName(apex)
	nodes := make([]nsec3Node, 0, len(names))
	for _, n := range names {
		if optOut && n.OptedOut {
			// Insecure delegations are elided from the chain when
			// opt-out is in effect (RFC 5155 §6). Their parent's
			// NSEC3 record then covers the gap, which is exactly
			// what saves the per-delegation NSEC3 cost on
			// delegation-heavy zones.
			continue
		}
		nodes = append(nodes, nsec3Node{
			Hash:      nsec3Hash(n.Name, salt, iterations),
			OwnerName: dns.CanonicalName(n.Name),
			Types:     sortedTypes(n.Types),
			OptedOut:  n.OptedOut,
		})
	}
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Hash < nodes[j].Hash
	})
	return &chain{
		Apex:       apexCanon,
		Salt:       salt,
		Iterations: iterations,
		OptOut:     optOut,
		nodes:      nodes,
	}
}

// hashName is a thin convenience for callers that need the same
// hash the chain uses (denial.go computes the hash of QNAME, the
// closest encloser, and the wildcard form). Centralizing here keeps
// the salt/iterations parameters — and the lowercase normalization
// — in one place.
func (c *chain) hashName(name string) string {
	return nsec3Hash(name, c.Salt, c.Iterations)
}

// ownerForNode returns the full DNS name where the NSEC3 RR for n
// lives in the zone — `<base32hex>.<apex>`. Used by denial.go when
// emitting the RR; Apex already carries the trailing dot so the
// concatenation is clean.
func (c *chain) ownerForNode(n *nsec3Node) string {
	return n.Hash + "." + c.Apex
}

// matchingNSEC3 returns the chain node whose hash equals hash(qname),
// or nil when no node matches. The denial path tries matching first
// (NODATA proofs need it) and falls back to covering for NXDOMAIN.
func (c *chain) matchingNSEC3(qname string) *nsec3Node {
	if len(c.nodes) == 0 {
		return nil
	}
	h := c.hashName(qname)
	i := sort.Search(len(c.nodes), func(i int) bool {
		return c.nodes[i].Hash >= h
	})
	if i < len(c.nodes) && c.nodes[i].Hash == h {
		return &c.nodes[i]
	}
	return nil
}

// coveringNSEC3 returns the chain node N such that
// N.Hash < hash(qname) < next(N).Hash in circular order, where next()
// is the natural successor and wraps from last to first. This is the
// "covering NSEC3 RR" of RFC 5155 §7.2 — the one that proves no
// hash equal to hash(qname) exists in the chain.
//
// Returns nil only when the chain is empty. The exact-match case
// (hash(qname) == some node's hash) is degenerate for covering — the
// caller should already have routed through matchingNSEC3. We
// nevertheless return the node just before the match so the
// covering invariant (N.Hash < h with wraparound) is preserved.
func (c *chain) coveringNSEC3(qname string) *nsec3Node {
	if len(c.nodes) == 0 {
		return nil
	}
	h := c.hashName(qname)
	i := sort.Search(len(c.nodes), func(i int) bool {
		return c.nodes[i].Hash >= h
	})
	// Wraparound: both `h < min(hashes)` (i == 0) and
	// `h > max(hashes)` (i == len) point at the chain's last node
	// — its .Next wraps from end to beginning and steps over h.
	if i == 0 || i == len(c.nodes) {
		return &c.nodes[len(c.nodes)-1]
	}
	return &c.nodes[i-1]
}

// nextHashedOwner returns the hash that goes into the NSEC3 RR's
// "Next Hashed Owner Name" field — RFC 5155 §3.1.7. For the last
// node in the chain this wraps to the first. Returns "" only when
// the chain is empty.
func (c *chain) nextHashedOwner(n *nsec3Node) string {
	if len(c.nodes) == 0 {
		return ""
	}
	// Find n in the slice by hash. The chain is small enough per
	// zone that binary search is fine even though we already have
	// a pointer — this avoids exposing an index field on nsec3Node.
	i := sort.Search(len(c.nodes), func(i int) bool {
		return c.nodes[i].Hash >= n.Hash
	})
	// Defensive: if n isn't in the chain at all, fall back to the
	// first node. Shouldn't happen in practice because callers
	// always pull n from matching/coveringNSEC3.
	if i == len(c.nodes) || c.nodes[i].Hash != n.Hash {
		return c.nodes[0].Hash
	}
	next := (i + 1) % len(c.nodes)
	return c.nodes[next].Hash
}

// wildcardSource returns the synthetic wildcard owner name that
// would have matched `owner` — i.e. `*.<closest-encloser-of-owner>`
// — when `owner` itself isn't in the chain but a matching wildcard
// IS. Returns "" when `owner` is a concrete chain entry or no
// matching wildcard ancestor exists.
//
// Two consumers:
//
//   - The signer reads it to set RRSIG.Labels correctly (RFC 4034
//     §3.1.3: Labels is the wildcard owner's label count minus 1).
//   - The wildcard-proof attacher reads it to decide when to emit
//     the §7.2.4 covering NSEC3 in the authority section.
//
// The function depends on the chain having ENTs for the wildcard's
// ancestors — synthesizeENTs in zone.go produces those, so a
// `*.dev.example.test.` wildcard works even when `dev.example.test.`
// has no explicit records of its own.
func (c *chain) wildcardSource(owner string) string {
	if c.matchingNSEC3(owner) != nil {
		return ""
	}
	encloser, _ := c.findClosestEncloser(owner)
	candidate := "*." + encloser
	if c.matchingNSEC3(candidate) != nil {
		return candidate
	}
	return ""
}

// sortedTypes returns a freshly-allocated, ascending-sorted copy of
// the input. The defensive copy stops the caller's slice from
// changing under us, and the sort matches the NSEC3 type bitmap
// encoding order (RFC 4034 §4.1.2 requires ascending).
func sortedTypes(in []uint16) []uint16 {
	if len(in) == 0 {
		return nil
	}
	out := make([]uint16, len(in))
	copy(out, in)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
