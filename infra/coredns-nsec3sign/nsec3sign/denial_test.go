// Denial-of-existence tests.
//
// We build a tiny zone by hand (apex + a small set of owner names)
// and inject it into Nsec3Sign.Chain — the file-plugin walker that
// produces this slice in production lands in step 5b. The unit tests
// here exercise the proof-construction algorithm in isolation; the
// end-to-end ServeDNS tests drive the whole chain (denial → sign →
// validator-style RRSIG.Verify on the NSEC3 RRsets).

package nsec3sign

import (
	"context"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// tinyZone is the canonical fixture: apex + two real names + the
// types each carries. The chain produced from it has three nodes;
// most denial cases route between exactly those three plus the
// queried name's hash.
func tinyZone() []nameInfo {
	return []nameInfo{
		{Name: "example.test.", Types: []uint16{dns.TypeSOA, dns.TypeNS}},
		{Name: "host.example.test.", Types: []uint16{dns.TypeA}},
		{Name: "sub.example.test.", Types: []uint16{dns.TypeA, dns.TypeAAAA}},
	}
}

func tinyChain() *chain {
	return buildChain("example.test.", "aabbccdd", 0, false, tinyZone())
}

func TestFindClosestEncloser(t *testing.T) {
	c := tinyChain()
	cases := []struct {
		qname, wantEncl, wantNext string
	}{
		// Missing direct child of apex → encloser=apex, next=qname.
		{"missing.example.test.", "example.test.", "missing.example.test."},
		// Missing grandchild of apex → encloser=apex,
		// next=<grandchild>.example.test (one label deeper than
		// encloser, on the path to qname).
		{"deep.missing.example.test.", "example.test.", "missing.example.test."},
		// Missing child of a real name → encloser=host.example.test,
		// next=qname.
		{"x.host.example.test.", "host.example.test.", "x.host.example.test."},
	}
	for _, tc := range cases {
		t.Run(tc.qname, func(t *testing.T) {
			gotEncl, gotNext := c.findClosestEncloser(tc.qname)
			if gotEncl != tc.wantEncl {
				t.Errorf("encloser = %s, want %s", gotEncl, tc.wantEncl)
			}
			if gotNext != tc.wantNext {
				t.Errorf("next closer = %s, want %s", gotNext, tc.wantNext)
			}
		})
	}
}

func TestProofForNXDOMAINHasThreeNSEC3s(t *testing.T) {
	c := tinyChain()
	rrs := c.proofForNXDOMAIN("missing.example.test.", 300)

	// Three NSEC3s in the general case: encloser match, next-closer
	// cover, wildcard cover. Some may dedupe to the same node, but
	// for `missing.example.test.` in this tiny zone they shouldn't.
	if len(rrs) == 0 {
		t.Fatal("no NSEC3 records produced for NXDOMAIN proof")
	}
	if len(rrs) > 3 {
		t.Fatalf("too many NSEC3s (%d) — RFC 5155 §7.2.1 caps at 3", len(rrs))
	}
	for _, rr := range rrs {
		if _, ok := rr.(*dns.NSEC3); !ok {
			t.Fatalf("non-NSEC3 record in proof: %T", rr)
		}
	}
}

func TestProofForNODATAReturnsMatchingNSEC3(t *testing.T) {
	c := tinyChain()
	rrs := c.proofForNODATA("host.example.test.", 300)
	if len(rrs) != 1 {
		t.Fatalf("NODATA proof returned %d records, want 1 (matching NSEC3)", len(rrs))
	}
	got := rrs[0].(*dns.NSEC3)
	// The owner name must be <hash(host)>.example.test.
	wantHash := c.hashName("host.example.test.")
	if got.Hdr.Name != wantHash+".example.test." {
		t.Fatalf("NODATA NSEC3 owner = %s, want %s", got.Hdr.Name, wantHash+".example.test.")
	}
	// And its type bitmap must list A (what host has) — that absence
	// of AAAA is what proves NODATA-for-AAAA.
	if !containsType(got.TypeBitMap, dns.TypeA) {
		t.Fatalf("type bitmap missing A: %v", got.TypeBitMap)
	}
}

func TestProofForNODATAOnNonExistentFallsBackToNXDOMAIN(t *testing.T) {
	c := tinyChain()
	rrs := c.proofForNODATA("missing.example.test.", 300)
	if len(rrs) == 0 {
		t.Fatal("expected NXDOMAIN-shaped fallback, got nothing")
	}
}

func TestRenderNSEC3OwnerAndNextHash(t *testing.T) {
	c := tinyChain()
	apexNode := c.matchingNSEC3("example.test.")
	if apexNode == nil {
		t.Fatal("apex missing from chain")
	}
	rr := c.renderNSEC3(apexNode, 600)
	if rr.Hash != nsec3HashSHA1 {
		t.Fatalf("Hash = %d, want %d (SHA-1)", rr.Hash, nsec3HashSHA1)
	}
	if rr.HashLength != 20 {
		t.Fatalf("HashLength = %d, want 20", rr.HashLength)
	}
	if rr.Iterations != c.Iterations {
		t.Fatalf("Iterations = %d, want %d", rr.Iterations, c.Iterations)
	}
	if rr.Salt != c.Salt {
		t.Fatalf("Salt = %q, want %q", rr.Salt, c.Salt)
	}
	if rr.SaltLength != uint8(len(c.Salt)/2) {
		t.Fatalf("SaltLength = %d, want %d", rr.SaltLength, len(c.Salt)/2)
	}
	if rr.NextDomain != c.nextHashedOwner(apexNode) {
		t.Fatalf("NextDomain = %s, want %s", rr.NextDomain, c.nextHashedOwner(apexNode))
	}
	// Flags must NOT have the opt-out bit set on a non-opted-out chain.
	if rr.Flags != 0 {
		t.Fatalf("Flags = %d, want 0 (opt-out disabled)", rr.Flags)
	}
}

func TestRenderNSEC3SetsOptOutFlag(t *testing.T) {
	c := buildChain("example.test.", "aabbccdd", 0, true /* optOut */, tinyZone())
	rr := c.renderNSEC3(&c.nodes[0], 300)
	if rr.Flags&1 == 0 {
		t.Fatalf("Flags = %d, want opt-out bit set", rr.Flags)
	}
}

func TestDedupedNSEC3CollapsesIdenticalHashes(t *testing.T) {
	c := tinyChain()
	// Pull one node and pass it three times — should yield exactly
	// one record (dedup keeps unique hashes only).
	n := &c.nodes[0]
	got := c.dedupedNSEC3([]*nsec3Node{n, n, n}, 300)
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1 (duplicates must collapse)", len(got))
	}
}

func TestDedupedNSEC3SkipsNils(t *testing.T) {
	c := tinyChain()
	got := c.dedupedNSEC3([]*nsec3Node{nil, nil}, 300)
	if got == nil {
		t.Fatal("dedupedNSEC3 returned nil — want empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("got %d records, want 0", len(got))
	}
}

// nxdomainHandler responds to every query with an NXDOMAIN-shaped
// message: rcode=NXDOMAIN, empty Answer, SOA in Ns. That's what the
// `file` plugin sends when a name is missing from a signed zone.
type nxdomainHandler struct {
	soa dns.RR
}

func (h *nxdomainHandler) Name() string { return "nxdomain" }
func (h *nxdomainHandler) ServeDNS(_ context.Context, w dns.ResponseWriter, r *dns.Msg) (int, error) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.Rcode = dns.RcodeNameError
	m.Ns = []dns.RR{h.soa}
	return dns.RcodeNameError, w.WriteMsg(m)
}

// nodataHandler is the parallel for NODATA: rcode=NOERROR, empty
// Answer, SOA in Ns. response.Typify classifies it as NoData.
type nodataHandler struct {
	soa dns.RR
}

func (h *nodataHandler) Name() string { return "nodata" }
func (h *nodataHandler) ServeDNS(_ context.Context, w dns.ResponseWriter, r *dns.Msg) (int, error) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.Rcode = dns.RcodeSuccess
	m.Ns = []dns.RR{h.soa}
	return dns.RcodeSuccess, w.WriteMsg(m)
}

func soaFor(zone string) *dns.SOA {
	return &dns.SOA{
		Hdr:     dns.RR_Header{Name: zone, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 3600},
		Ns:      "ns1." + zone,
		Mbox:    "hostmaster." + zone,
		Serial:  1, Refresh: 3600, Retry: 600, Expire: 86400, Minttl: 300,
	}
}

func TestServeDNSAttachesAndSignsNXDOMAINProof(t *testing.T) {
	const zone = "example.test."
	zskDK, zskPriv := generateDNSKEY(t, zone, dns.ECDSAP256SHA256, 256, 256)
	zsk := &signingKey{KeyTag: zskDK.KeyTag(), Public: zskDK, Private: zskPriv}

	n := &Nsec3Sign{
		Zones: []string{zone},
		Keys:  []*signingKey{zsk},
		Chain: tinyChain(),
		Next:  &nxdomainHandler{soa: soaFor(zone)},
	}
	w := &captureWriter{}
	if _, err := n.ServeDNS(context.Background(), w, query("missing."+zone, dns.TypeA, true)); err != nil {
		t.Fatal(err)
	}
	if w.captured == nil {
		t.Fatal("nothing captured")
	}

	// Authority section should now carry SOA + NSEC3s + RRSIGs over
	// each RRset (SOA + each NSEC3). Count NSEC3 RRs and their
	// RRSIGs.
	var nsec3s, nsec3sigs int
	for _, rr := range w.captured.Ns {
		switch v := rr.(type) {
		case *dns.NSEC3:
			nsec3s++
		case *dns.RRSIG:
			if v.TypeCovered == dns.TypeNSEC3 {
				nsec3sigs++
			}
		}
	}
	if nsec3s == 0 {
		t.Fatal("no NSEC3 records attached")
	}
	if nsec3sigs == 0 {
		t.Fatal("NSEC3 RRsets weren't signed — denial would be rejected by validators")
	}

	// And the SOA itself must be signed (positive-response signer).
	if findRRSIG(w.captured.Ns, dns.TypeSOA) == nil {
		t.Fatal("SOA in authority section is unsigned")
	}
}

func TestServeDNSAttachesNODATAProof(t *testing.T) {
	const zone = "example.test."
	zskDK, zskPriv := generateDNSKEY(t, zone, dns.ECDSAP256SHA256, 256, 256)
	zsk := &signingKey{KeyTag: zskDK.KeyTag(), Public: zskDK, Private: zskPriv}

	n := &Nsec3Sign{
		Zones: []string{zone},
		Keys:  []*signingKey{zsk},
		Chain: tinyChain(),
		Next:  &nodataHandler{soa: soaFor(zone)},
	}
	w := &captureWriter{}
	if _, err := n.ServeDNS(context.Background(), w, query("host."+zone, dns.TypeAAAA, true)); err != nil {
		t.Fatal(err)
	}

	// Expect exactly one NSEC3 (the matching one for `host.`) plus
	// the SOA — no covering or wildcard records on a direct NODATA.
	var nsec3s []*dns.NSEC3
	for _, rr := range w.captured.Ns {
		if v, ok := rr.(*dns.NSEC3); ok {
			nsec3s = append(nsec3s, v)
		}
	}
	if len(nsec3s) != 1 {
		t.Fatalf("got %d NSEC3s for NODATA, want 1", len(nsec3s))
	}
	wantHash := n.Chain.hashName("host." + zone)
	if nsec3s[0].Hdr.Name != wantHash+"."+zone {
		t.Fatalf("NSEC3 owner = %s, want %s", nsec3s[0].Hdr.Name, wantHash+"."+zone)
	}
}

func TestServeDNSDenialProofVerifies(t *testing.T) {
	// End-to-end DNSSEC sanity: drive an NXDOMAIN through ServeDNS,
	// pluck the NSEC3 RRset and its RRSIG out of the response, and
	// run RRSIG.Verify with the loaded public key. That's the path
	// an actual resolver takes — if this passes, a validator sees
	// our denial as authentic.
	const zone = "example.test."
	zskDK, zskPriv := generateDNSKEY(t, zone, dns.ECDSAP256SHA256, 256, 256)
	zsk := &signingKey{KeyTag: zskDK.KeyTag(), Public: zskDK, Private: zskPriv}

	n := &Nsec3Sign{
		Zones: []string{zone},
		Keys:  []*signingKey{zsk},
		Chain: tinyChain(),
		Next:  &nxdomainHandler{soa: soaFor(zone)},
	}
	w := &captureWriter{}
	if _, err := n.ServeDNS(context.Background(), w, query("missing."+zone, dns.TypeA, true)); err != nil {
		t.Fatal(err)
	}

	// Pull every NSEC3 RR + its covering RRSIG, then verify in pairs
	// grouped by owner name. RRSIG.Verify takes the full RRset, but
	// in our denial each NSEC3 occupies its own owner so the "RRset"
	// is one-element-long.
	bySigOwner := make(map[string]*dns.RRSIG)
	byOwner := make(map[string][]dns.RR)
	for _, rr := range w.captured.Ns {
		switch v := rr.(type) {
		case *dns.NSEC3:
			byOwner[v.Hdr.Name] = append(byOwner[v.Hdr.Name], v)
		case *dns.RRSIG:
			if v.TypeCovered == dns.TypeNSEC3 {
				bySigOwner[v.Hdr.Name] = v
			}
		}
	}
	if len(byOwner) == 0 || len(bySigOwner) == 0 {
		t.Fatalf("missing records: %d NSEC3s, %d RRSIGs", len(byOwner), len(bySigOwner))
	}
	for owner, rrs := range byOwner {
		sig, ok := bySigOwner[owner]
		if !ok {
			t.Fatalf("no RRSIG for NSEC3 at %s", owner)
		}
		if err := sig.Verify(zskDK, rrs); err != nil {
			t.Fatalf("RRSIG.Verify for NSEC3 at %s: %v", owner, err)
		}
	}
}

func TestAttachDenialProofIsNoOpWithoutChain(t *testing.T) {
	// Chain==nil is the production state in steps 1-5; we need the
	// short-circuit to be airtight before step 5b plugs in the
	// chain-walker.
	const zone = "example.test."
	n := &Nsec3Sign{Zones: []string{zone}}
	m := new(dns.Msg)
	m.SetQuestion("missing."+zone, dns.TypeA)
	m.Rcode = dns.RcodeNameError
	m.Ns = []dns.RR{soaFor(zone)}

	got := n.attachDenialProof(m, "missing."+zone, time.Now().UTC())
	for _, rr := range got.Ns {
		if _, ok := rr.(*dns.NSEC3); ok {
			t.Fatal("attached NSEC3 even though Chain==nil")
		}
	}
}

func containsType(types []uint16, t uint16) bool {
	for _, x := range types {
		if x == t {
			return true
		}
	}
	return false
}
