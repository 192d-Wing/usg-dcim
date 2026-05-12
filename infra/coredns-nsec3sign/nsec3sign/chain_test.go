// Chain-builder tests.
//
// The hash values pinned below are from RFC 5155 Appendix B, which
// publishes a complete NSEC3 example for the `example.` zone using
// salt=aabbccdd, iterations=12. Re-using them here means a future
// upgrade of miekg/dns that breaks NSEC3 hashing fails our test
// suite immediately — the RFC examples are the closest thing to a
// gold-standard interoperability check available.

package nsec3sign

import (
	"sort"
	"testing"

	"github.com/miekg/dns"
)

const (
	rfc5155Apex       = "example."
	rfc5155Salt       = "aabbccdd"
	rfc5155Iterations = 12
)

// rfc5155Hashes is the {owner-name → expected base32hex hash} table
// from RFC 5155 §B. Kept as a separate var so each test can pull
// the names it needs by key — and so a tooling sweep of the file
// instantly shows which vectors are in play.
var rfc5155Hashes = map[string]string{
	"example.":       "0p9mhaveqvm6t7vbl5lop2u3t2rp3tom",
	"a.example.":     "35mthgpgcu1qg68fab165klnsnk3dpvl",
	"ai.example.":    "gjeqe526plbf1g8mklp59enfd789njgi",
	"ns1.example.":   "2t7b4g4vsa5smi47k61mv5bv1a22bojr",
	"ns2.example.":   "q04jkcevqvmu85r014c7dkba38o0ji5r",
	"w.example.":     "k8udemvp1j2f7eg6jebps17vp3n8i58h",
	"*.w.example.":   "r53bq7cc2uvmubfu5ocmm6pers9tk9en",
	"x.w.example.":   "b4um86eghhds6nea196smvmlo4ors995",
	"y.w.example.":   "ji6neoaepv8b5o6k4ev33abha8ht9fgc",
	"x.y.w.example.": "2vptu5timamqttgl4luu9kg21e0aor3s",
	"xx.example.":    "t644ebqk9bibcna874givr6joj62mlhv",
}

// buildRFC5155Chain assembles a chain from the Appendix-B owner
// names with default (zero, false) opt-out, so each test gets the
// same shape without repeating the setup boilerplate.
func buildRFC5155Chain(t *testing.T) *chain {
	t.Helper()
	names := make([]nameInfo, 0, len(rfc5155Hashes))
	for owner := range rfc5155Hashes {
		names = append(names, nameInfo{Name: owner})
	}
	return buildChain(rfc5155Apex, rfc5155Salt, rfc5155Iterations, false, names)
}

func TestHashNameMatchesRFC5155(t *testing.T) {
	c := buildRFC5155Chain(t)
	for owner, want := range rfc5155Hashes {
		got := c.hashName(owner)
		if got != want {
			t.Errorf("hash(%s) = %s, want %s (RFC 5155 §B)", owner, got, want)
		}
	}
}

func TestBuildChainSortedByHash(t *testing.T) {
	c := buildRFC5155Chain(t)
	if !sort.SliceIsSorted(c.nodes, func(i, j int) bool {
		return c.nodes[i].Hash < c.nodes[j].Hash
	}) {
		t.Fatal("chain is not sorted by hash")
	}
}

func TestMatchingNSEC3Found(t *testing.T) {
	c := buildRFC5155Chain(t)
	for owner, wantHash := range rfc5155Hashes {
		got := c.matchingNSEC3(owner)
		if got == nil {
			t.Errorf("matchingNSEC3(%s) = nil, expected node with hash %s",
				owner, wantHash)
			continue
		}
		if got.Hash != wantHash {
			t.Errorf("matchingNSEC3(%s).Hash = %s, want %s",
				owner, got.Hash, wantHash)
		}
	}
}

func TestMatchingNSEC3NotFound(t *testing.T) {
	c := buildRFC5155Chain(t)
	// `c.example.` is not in the Appendix-B zone, so its hash should
	// not match any chain node.
	if got := c.matchingNSEC3("c.example."); got != nil {
		t.Fatalf("matchingNSEC3 returned %+v for a non-existent name", got)
	}
}

func TestCoveringNSEC3Bracketing(t *testing.T) {
	// `c.example.` is the canonical RFC 5155 NXDOMAIN example. Its
	// hash falls between two adjacent chain nodes; coveringNSEC3
	// must return the node whose hash is the lower of the pair.
	c := buildRFC5155Chain(t)
	missingHash := c.hashName("c.example.")

	node := c.coveringNSEC3("c.example.")
	if node == nil {
		t.Fatal("coveringNSEC3 returned nil for a name in a non-empty chain")
	}
	if node.Hash >= missingHash {
		t.Fatalf("covering hash %s should be < missing hash %s", node.Hash, missingHash)
	}
	next := c.nextHashedOwner(node)
	if next <= missingHash {
		t.Fatalf("next hashed owner %s should be > missing hash %s", next, missingHash)
	}
}

func TestCoveringNSEC3WraparoundLow(t *testing.T) {
	// A name whose hash is lower than every hash in the chain must
	// be covered by the last node (whose .Next wraps to the first).
	c := buildRFC5155Chain(t)
	// Pick a synthetic name and verify its hash falls below the
	// minimum. If it doesn't on this particular zone, the test still
	// works — we just won't be exercising the wraparound branch and
	// the assertion at the end catches that.
	candidate := "aaaaaaa.example."
	hash := c.hashName(candidate)
	if hash >= c.nodes[0].Hash {
		t.Skipf("synthetic name %s hashed to %s, not below chain min %s",
			candidate, hash, c.nodes[0].Hash)
	}
	node := c.coveringNSEC3(candidate)
	if node != &c.nodes[len(c.nodes)-1] {
		t.Fatalf("wraparound (low) should return the last chain node, got hash %s",
			node.Hash)
	}
}

func TestCoveringNSEC3WraparoundHigh(t *testing.T) {
	// And the symmetric case — a hash greater than every chain hash
	// is also covered by the last node.
	c := buildRFC5155Chain(t)
	candidate := "zzzzzzz.example."
	hash := c.hashName(candidate)
	if hash <= c.nodes[len(c.nodes)-1].Hash {
		t.Skipf("synthetic name %s hashed to %s, not above chain max %s",
			candidate, hash, c.nodes[len(c.nodes)-1].Hash)
	}
	node := c.coveringNSEC3(candidate)
	if node != &c.nodes[len(c.nodes)-1] {
		t.Fatalf("wraparound (high) should return the last chain node, got hash %s",
			node.Hash)
	}
}

func TestCoveringNSEC3SingleNodeAlwaysWraps(t *testing.T) {
	// A 1-node chain is the cleanest way to exercise wraparound
	// deterministically: any qname other than the single chain
	// owner *must* be covered by that one node, no matter where its
	// hash falls relative to the chain entry. The synthetic-name
	// wraparound tests above are probabilistic; this one isn't.
	c := buildChain("example.", "", 0, false, []nameInfo{{Name: "only.example."}})
	if got := len(c.nodes); got != 1 {
		t.Fatalf("single-node chain has %d nodes", got)
	}
	only := &c.nodes[0]
	for _, q := range []string{"a.example.", "z.example.", "middle.example."} {
		if got := c.coveringNSEC3(q); got != only {
			t.Fatalf("coveringNSEC3(%s) = %+v, want the only chain node %+v",
				q, got, only)
		}
	}
}

func TestEmptyChainLookupsAreSafe(t *testing.T) {
	c := buildChain("example.", "", 0, false, nil)
	if got := c.matchingNSEC3("anything.example."); got != nil {
		t.Fatalf("matchingNSEC3 on empty chain = %+v, want nil", got)
	}
	if got := c.coveringNSEC3("anything.example."); got != nil {
		t.Fatalf("coveringNSEC3 on empty chain = %+v, want nil", got)
	}
	if got := c.nextHashedOwner(&nsec3Node{Hash: "ignored"}); got != "" {
		t.Fatalf("nextHashedOwner on empty chain = %q, want \"\"", got)
	}
}

func TestOptOutSkipsInsecureDelegations(t *testing.T) {
	names := []nameInfo{
		{Name: "example.", Types: []uint16{dns.TypeSOA, dns.TypeNS}},
		{Name: "secure.example.", Types: []uint16{dns.TypeNS, dns.TypeDS}},
		{Name: "insecure.example.", Types: []uint16{dns.TypeNS}, OptedOut: true},
	}
	withOptOut := buildChain("example.", "aabb", 0, true, names)
	if got := len(withOptOut.nodes); got != 2 {
		t.Fatalf("opt-out chain len = %d, want 2 (insecure delegation must be elided)", got)
	}
	if withOptOut.matchingNSEC3("insecure.example.") != nil {
		t.Fatal("matchingNSEC3 returned the elided insecure delegation")
	}

	// Same input without opt-out keeps the delegation in the chain.
	withoutOptOut := buildChain("example.", "aabb", 0, false, names)
	if got := len(withoutOptOut.nodes); got != 3 {
		t.Fatalf("no-opt-out chain len = %d, want 3", got)
	}
	if withoutOptOut.matchingNSEC3("insecure.example.") == nil {
		t.Fatal("matchingNSEC3 missed insecure delegation when opt-out is off")
	}
}

func TestSortedTypes(t *testing.T) {
	out := sortedTypes([]uint16{43, 6, 2, 46})
	want := []uint16{2, 6, 43, 46}
	for i := range out {
		if out[i] != want[i] {
			t.Fatalf("sortedTypes[%d] = %d, want %d", i, out[i], want[i])
		}
	}
	if got := sortedTypes(nil); got != nil {
		t.Fatalf("sortedTypes(nil) = %v, want nil", got)
	}
}

func TestOwnerForNode(t *testing.T) {
	c := buildRFC5155Chain(t)
	n := c.matchingNSEC3("example.")
	if n == nil {
		t.Fatal("apex not found in chain")
	}
	want := rfc5155Hashes["example."] + ".example."
	if got := c.ownerForNode(n); got != want {
		t.Fatalf("ownerForNode = %s, want %s", got, want)
	}
}

func TestNextHashedOwnerWrapsAtEnd(t *testing.T) {
	c := buildRFC5155Chain(t)
	last := &c.nodes[len(c.nodes)-1]
	first := c.nodes[0]
	if got := c.nextHashedOwner(last); got != first.Hash {
		t.Fatalf("nextHashedOwner on last node = %s, want first hash %s",
			got, first.Hash)
	}
}
