// End-to-end signing tests. Each scenario builds a stub plugin
// that returns a fixed message, wraps it in Nsec3Sign with a
// generated key, drives one query through ServeDNS, and verifies
// the resulting RRSIGs with `RRSIG.Verify` — the same path a real
// validator takes. Round-trip is the right shape here: an RRSIG
// that "looks right" but doesn't validate is the failure mode
// production would hit, and we want to catch it in unit tests.

package nsec3sign

import (
	"context"
	"testing"
	"time"

	"github.com/coredns/coredns/plugin/test"

	"github.com/miekg/dns"
)

// stubHandler is a minimal plugin.Handler for tests. ServeDNS
// writes a pre-built copy of the configured message in response to
// every query, with the request ID + question section stitched in
// via SetReply.
type stubHandler struct {
	answer []dns.RR
	ns     []dns.RR
	extra  []dns.RR
	rcode  int
}

func (h *stubHandler) Name() string { return "stub" }

func (h *stubHandler) ServeDNS(_ context.Context, w dns.ResponseWriter, r *dns.Msg) (int, error) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.Rcode = h.rcode
	m.Answer = append(m.Answer, h.answer...)
	m.Ns = append(m.Ns, h.ns...)
	m.Extra = append(m.Extra, h.extra...)
	return h.rcode, w.WriteMsg(m)
}

// captureWriter intercepts the final WriteMsg from ServeDNS so the
// test can introspect what would have gone on the wire.
type captureWriter struct {
	test.ResponseWriter
	captured *dns.Msg
}

func (w *captureWriter) WriteMsg(m *dns.Msg) error {
	w.captured = m
	return nil
}

// newPluginWithKey assembles an Nsec3Sign + stub handler pair with
// one freshly-generated ECDSA P-256 key. ECDSA is the default
// algorithm DCIM emits and is what most of the round-trip tests
// exercise; algorithm coverage is handled separately below.
func newPluginWithKey(t *testing.T, zone string, flags uint16, answer, ns []dns.RR) (*Nsec3Sign, *signingKey) {
	t.Helper()
	dk, priv := generateDNSKEY(t, zone, dns.ECDSAP256SHA256, 256, flags)
	key := &signingKey{
		KeyTag:  dk.KeyTag(),
		IsKSK:   flags&dns.SEP != 0,
		Public:  dk,
		Private: priv,
	}
	n := &Nsec3Sign{
		Zones: []string{zone},
		Keys:  []*signingKey{key},
		Next:  &stubHandler{answer: answer, ns: ns},
	}
	return n, key
}

// query builds a DNS request with the DO bit set (unless do=false)
// and an EDNS0 buffer size large enough that signing doesn't push
// us over.
func query(name string, qtype uint16, do bool) *dns.Msg {
	m := new(dns.Msg)
	m.SetQuestion(name, qtype)
	if do {
		m.SetEdns0(4096, true)
	}
	return m
}

// findRRSIG returns the first RRSIG in section that covers the
// given type, or nil if absent.
func findRRSIG(section []dns.RR, typeCovered uint16) *dns.RRSIG {
	for _, rr := range section {
		if sig, ok := rr.(*dns.RRSIG); ok && sig.TypeCovered == typeCovered {
			return sig
		}
	}
	return nil
}

func TestServeDNSSignsAnswerSection(t *testing.T) {
	const zone = "example.test."
	a := &dns.A{
		Hdr: dns.RR_Header{Name: "host." + zone, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
		A:   nil, // populated below — testIPv4 keeps the test data block tight
	}
	a.A = []byte{10, 0, 0, 1}

	n, key := newPluginWithKey(t, zone, 256, []dns.RR{a}, nil)
	w := &captureWriter{}
	code, err := n.ServeDNS(context.Background(), w, query("host."+zone, dns.TypeA, true))
	if err != nil {
		t.Fatalf("ServeDNS: %v", err)
	}
	if code != dns.RcodeSuccess {
		t.Fatalf("rcode = %d, want NOERROR", code)
	}
	if w.captured == nil {
		t.Fatal("nothing written")
	}

	sig := findRRSIG(w.captured.Answer, dns.TypeA)
	if sig == nil {
		t.Fatalf("no RRSIG over A in answer; got %d records", len(w.captured.Answer))
	}
	if sig.KeyTag != key.KeyTag {
		t.Fatalf("RRSIG.KeyTag = %d, want %d", sig.KeyTag, key.KeyTag)
	}
	if sig.SignerName != zone {
		t.Fatalf("RRSIG.SignerName = %s, want %s", sig.SignerName, zone)
	}
	if err := sig.Verify(key.Public, []dns.RR{a}); err != nil {
		t.Fatalf("RRSIG.Verify: %v", err)
	}
}

func TestServeDNSSignsAuthoritySection(t *testing.T) {
	const zone = "example.test."
	soa := &dns.SOA{
		Hdr:     dns.RR_Header{Name: zone, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 3600},
		Ns:      "ns1." + zone,
		Mbox:    "hostmaster." + zone,
		Serial:  1, Refresh: 3600, Retry: 600, Expire: 86400, Minttl: 300,
	}
	n, key := newPluginWithKey(t, zone, 256, nil, []dns.RR{soa})
	w := &captureWriter{}
	if _, err := n.ServeDNS(context.Background(), w, query("missing."+zone, dns.TypeA, true)); err != nil {
		t.Fatal(err)
	}
	sig := findRRSIG(w.captured.Ns, dns.TypeSOA)
	if sig == nil {
		t.Fatalf("no RRSIG over SOA in authority")
	}
	if err := sig.Verify(key.Public, []dns.RR{soa}); err != nil {
		t.Fatalf("RRSIG.Verify: %v", err)
	}
}

func TestServeDNSSignsDNSKEYWithKSK(t *testing.T) {
	const zone = "example.test."
	kskDK, kskPriv := generateDNSKEY(t, zone, dns.ECDSAP256SHA256, 256, 257)
	zskDK, zskPriv := generateDNSKEY(t, zone, dns.ECDSAP256SHA256, 256, 256)
	ksk := &signingKey{KeyTag: kskDK.KeyTag(), IsKSK: true, Public: kskDK, Private: kskPriv}
	zsk := &signingKey{KeyTag: zskDK.KeyTag(), IsKSK: false, Public: zskDK, Private: zskPriv}

	dnskeyRRset := []dns.RR{kskDK, zskDK}
	n := &Nsec3Sign{
		Zones: []string{zone},
		Keys:  []*signingKey{ksk, zsk},
		Next:  &stubHandler{answer: dnskeyRRset},
	}

	w := &captureWriter{}
	if _, err := n.ServeDNS(context.Background(), w, query(zone, dns.TypeDNSKEY, true)); err != nil {
		t.Fatal(err)
	}
	sig := findRRSIG(w.captured.Answer, dns.TypeDNSKEY)
	if sig == nil {
		t.Fatal("no RRSIG over DNSKEY")
	}
	// DNSKEY RRsets must be signed by the KSK (the chain validators
	// follow back to the parent's DS). Tag must match KSK, not ZSK.
	if sig.KeyTag != ksk.KeyTag {
		t.Fatalf("DNSKEY signed by tag %d, want KSK %d", sig.KeyTag, ksk.KeyTag)
	}
	if err := sig.Verify(kskDK, dnskeyRRset); err != nil {
		t.Fatalf("RRSIG.Verify with KSK: %v", err)
	}
}

func TestServeDNSPassesThroughWhenDOMissing(t *testing.T) {
	const zone = "example.test."
	a := &dns.A{
		Hdr: dns.RR_Header{Name: "host." + zone, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
		A:   []byte{10, 0, 0, 2},
	}
	n, _ := newPluginWithKey(t, zone, 256, []dns.RR{a}, nil)

	w := &captureWriter{}
	if _, err := n.ServeDNS(context.Background(), w, query("host."+zone, dns.TypeA, false)); err != nil {
		t.Fatal(err)
	}
	if findRRSIG(w.captured.Answer, dns.TypeA) != nil {
		t.Fatal("RRSIG appended despite DO=0 — RFC 6840 §5.9 violation")
	}
}

func TestServeDNSPassesThroughOutsideZone(t *testing.T) {
	const zone = "example.test."
	a := &dns.A{
		Hdr: dns.RR_Header{Name: "host.other.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
		A:   []byte{10, 0, 0, 3},
	}
	n, _ := newPluginWithKey(t, zone, 256, []dns.RR{a}, nil)

	w := &captureWriter{}
	if _, err := n.ServeDNS(context.Background(), w, query("host.other.", dns.TypeA, true)); err != nil {
		t.Fatal(err)
	}
	if findRRSIG(w.captured.Answer, dns.TypeA) != nil {
		t.Fatal("out-of-zone query was signed")
	}
}

func TestServeDNSPassesThroughWithNoKeys(t *testing.T) {
	const zone = "example.test."
	a := &dns.A{
		Hdr: dns.RR_Header{Name: "host." + zone, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
		A:   []byte{10, 0, 0, 4},
	}
	// Bypass newPluginWithKey so we can leave Keys empty.
	n := &Nsec3Sign{
		Zones: []string{zone},
		Next:  &stubHandler{answer: []dns.RR{a}},
	}
	w := &captureWriter{}
	if _, err := n.ServeDNS(context.Background(), w, query("host."+zone, dns.TypeA, true)); err != nil {
		t.Fatal(err)
	}
	if findRRSIG(w.captured.Answer, dns.TypeA) != nil {
		t.Fatal("no-key configuration produced a signature")
	}
}

func TestSignerProducesPerKeySignatures(t *testing.T) {
	// Two ZSKs loaded → one RRSIG per ZSK, both must verify.
	const zone = "example.test."
	zsk1DK, zsk1Priv := generateDNSKEY(t, zone, dns.ECDSAP256SHA256, 256, 256)
	zsk2DK, zsk2Priv := generateDNSKEY(t, zone, dns.ED25519, 256, 256)
	zsk1 := &signingKey{KeyTag: zsk1DK.KeyTag(), Public: zsk1DK, Private: zsk1Priv}
	zsk2 := &signingKey{KeyTag: zsk2DK.KeyTag(), Public: zsk2DK, Private: zsk2Priv}

	a := &dns.A{
		Hdr: dns.RR_Header{Name: "host." + zone, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
		A:   []byte{10, 0, 0, 5},
	}
	n := &Nsec3Sign{
		Zones: []string{zone},
		Keys:  []*signingKey{zsk1, zsk2},
		Next:  &stubHandler{answer: []dns.RR{a}},
	}

	w := &captureWriter{}
	if _, err := n.ServeDNS(context.Background(), w, query("host."+zone, dns.TypeA, true)); err != nil {
		t.Fatal(err)
	}

	var sigs []*dns.RRSIG
	for _, rr := range w.captured.Answer {
		if sig, ok := rr.(*dns.RRSIG); ok && sig.TypeCovered == dns.TypeA {
			sigs = append(sigs, sig)
		}
	}
	if len(sigs) != 2 {
		t.Fatalf("got %d RRSIGs, want 2 (one per ZSK)", len(sigs))
	}
	for _, sig := range sigs {
		var pub *dns.DNSKEY
		switch sig.KeyTag {
		case zsk1.KeyTag:
			pub = zsk1DK
		case zsk2.KeyTag:
			pub = zsk2DK
		default:
			t.Fatalf("RRSIG with unknown keytag %d", sig.KeyTag)
		}
		if err := sig.Verify(pub, []dns.RR{a}); err != nil {
			t.Fatalf("RRSIG (keytag %d) failed to verify: %v", sig.KeyTag, err)
		}
	}
}

func TestSignerInceptionExpirationWindow(t *testing.T) {
	// The constants are documented as "inception = now - 1h,
	// expiration = now + 8 days"; pin them so a future change to
	// either value comes with a test update.
	const zone = "example.test."
	a := &dns.A{
		Hdr: dns.RR_Header{Name: "host." + zone, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
		A:   []byte{10, 0, 0, 6},
	}
	n, _ := newPluginWithKey(t, zone, 256, []dns.RR{a}, nil)
	w := &captureWriter{}

	before := time.Now().UTC()
	if _, err := n.ServeDNS(context.Background(), w, query("host."+zone, dns.TypeA, true)); err != nil {
		t.Fatal(err)
	}
	after := time.Now().UTC()

	sig := findRRSIG(w.captured.Answer, dns.TypeA)
	if sig == nil {
		t.Fatal("no RRSIG produced")
	}
	wantIncepLow := uint32(before.Add(-sigInceptionOffset - 1*time.Second).Unix())
	wantIncepHigh := uint32(after.Add(-sigInceptionOffset + 1*time.Second).Unix())
	if sig.Inception < wantIncepLow || sig.Inception > wantIncepHigh {
		t.Fatalf("inception %d outside [%d, %d]", sig.Inception, wantIncepLow, wantIncepHigh)
	}
	wantExpirLow := uint32(before.Add(sigValidity - 1*time.Second).Unix())
	wantExpirHigh := uint32(after.Add(sigValidity + 1*time.Second).Unix())
	if sig.Expiration < wantExpirLow || sig.Expiration > wantExpirHigh {
		t.Fatalf("expiration %d outside [%d, %d]", sig.Expiration, wantExpirLow, wantExpirHigh)
	}
}

func TestGroupByRRsetSkipsSigsAndOPT(t *testing.T) {
	a := &dns.A{
		Hdr: dns.RR_Header{Name: "a.example.test.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
		A:   []byte{10, 0, 0, 1},
	}
	sig := &dns.RRSIG{
		Hdr:         dns.RR_Header{Name: "a.example.test.", Rrtype: dns.TypeRRSIG, Class: dns.ClassINET, Ttl: 300},
		TypeCovered: dns.TypeA,
	}
	opt := new(dns.OPT)
	opt.Hdr.Name = "."
	opt.Hdr.Rrtype = dns.TypeOPT

	got := groupByRRset([]dns.RR{a, sig, opt})
	if len(got) != 1 {
		t.Fatalf("got %d groups, want 1 (A only — RRSIG and OPT skipped)", len(got))
	}
}

func TestRRSIGLabels(t *testing.T) {
	cases := map[string]uint8{
		"example.":         1,
		"host.example.":    2,
		"*.example.":       1, // RFC 4034 §3.1.3 — wildcard label not counted
		"*.x.y.example.":   3,
		".":                0,
	}
	for name, want := range cases {
		if got := rrsigLabels(name); got != want {
			t.Errorf("rrsigLabels(%q) = %d, want %d", name, got, want)
		}
	}
}
