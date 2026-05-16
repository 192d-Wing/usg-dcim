// Zone-file ingestion tests.
//
// Each test drops a small BIND-format zone into a temp file, runs
// it through parseZoneFile, and asserts the shape of the resulting
// nameInfo slice + detected apex. The end-to-end signed-denial test
// adds keys + ServeDNS to prove the wiring all the way through —
// parse → chain → denial proof → RRSIG.Verify against the loaded
// public key, the same path a validator takes.

package nsec3sign

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/miekg/dns"
)

// flatZone is a tiny but realistic DCIM-style zone — apex + a few
// hosts, one insecure delegation, one secure delegation. Used by
// most parser tests; the end-to-end test extends it.
const flatZone = `$ORIGIN example.test.
$TTL 3600
@         IN SOA  ns1.example.test. hostmaster.example.test. 2026051200 3600 600 86400 300
@         IN NS   ns1.example.test.
ns1       IN A    10.0.0.1
host      IN A    10.0.0.2
host      IN AAAA fd00::2
secure    IN NS   ns.secure.example.test.
secure    IN DS   12345 13 2 ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789
insecure  IN NS   ns.insecure.example.test.
`

// writeZone drops content into a temp zone file and returns the
// path. Centralized so any future zone-file flag (permissions,
// naming) lives in one place.
func writeZone(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "example.test.zone")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// hierarchicalZone has a record three labels deep with NO records
// at the intermediate `floor3` label. `floor3.example.test.` is an
// empty non-terminal (ENT) — required to be in the NSEC3 chain per
// RFC 5155 §7.2.2 so direct queries for it return a verifiable
// NODATA proof instead of looking like NXDOMAIN.
const hierarchicalZone = `$ORIGIN example.test.
$TTL 3600
@                              IN SOA  ns1.example.test. hostmaster.example.test. 1 3600 600 86400 300
@                              IN NS   ns1.example.test.
ns1                            IN A    10.0.0.1
printer-1.floor3.building-a    IN A    10.0.0.41
printer-2.floor3.building-a    IN A    10.0.0.42
`

func TestParseZoneFileSynthesizesEmptyNonTerminals(t *testing.T) {
	path := writeZone(t, hierarchicalZone)
	names, _, err := parseZoneFile(path)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]nameInfo)
	for _, n := range names {
		byName[n.Name] = n
	}
	// `floor3.building-a.example.test.` and `building-a.example.test.`
	// are ENTs — they have descendants but no records of their own.
	// Both must be synthesized into the chain so direct queries for
	// them return NODATA, not NXDOMAIN.
	for _, ent := range []string{
		"floor3.building-a.example.test.",
		"building-a.example.test.",
	} {
		info, ok := byName[ent]
		if !ok {
			t.Errorf("ENT %s missing from parsed names", ent)
			continue
		}
		// ENTs have empty type bitmaps — the absence of types is
		// what the validator checks. A non-empty Types slice here
		// would mean we accidentally promoted the ENT to a regular
		// owner (e.g. by adding RRSIG unconditionally).
		if len(info.Types) != 0 {
			t.Errorf("ENT %s has Types %v, want empty", ent, info.Types)
		}
		if info.OptedOut {
			t.Errorf("ENT %s flagged OptedOut — ENTs have no NS records", ent)
		}
	}
	// Apex must NOT be flagged as an ENT — it's an explicit owner.
	apex := byName["example.test."]
	if len(apex.Types) == 0 {
		t.Error("apex emitted with empty Types — should be explicit owner with SOA/NS/RRSIG")
	}
	// Sanity: the explicit leaf names still appear.
	for _, leaf := range []string{
		"printer-1.floor3.building-a.example.test.",
		"printer-2.floor3.building-a.example.test.",
	} {
		if _, ok := byName[leaf]; !ok {
			t.Errorf("explicit leaf %s missing from parsed names", leaf)
		}
	}
}

func TestLoadChainENTsAreMatchable(t *testing.T) {
	// After loadChain runs, matchingNSEC3 against an ENT name must
	// return a node — that's the proof shape a query for the ENT
	// itself relies on. Validators see "matching NSEC3 with empty
	// bitmap" and interpret it as NODATA-at-ENT.
	n := &Nsec3Sign{
		Salt:     "aabbccdd",
		ZoneFile: writeZone(t, hierarchicalZone),
	}
	if err := n.loadChain(); err != nil {
		t.Fatal(err)
	}
	node := n.Chain.matchingNSEC3("floor3.building-a.example.test.")
	if node == nil {
		t.Fatal("ENT floor3.building-a.example.test. missing from chain")
	}
	if len(node.Types) != 0 {
		t.Fatalf("ENT chain node has Types %v, want empty", node.Types)
	}
}

func TestParseZoneFileApexDetected(t *testing.T) {
	path := writeZone(t, flatZone)
	_, apex, err := parseZoneFile(path)
	if err != nil {
		t.Fatalf("parseZoneFile: %v", err)
	}
	if apex != "example.test." {
		t.Fatalf("apex = %q, want %q", apex, "example.test.")
	}
}

func TestParseZoneFileOwnersAndTypes(t *testing.T) {
	path := writeZone(t, flatZone)
	names, _, err := parseZoneFile(path)
	if err != nil {
		t.Fatal(err)
	}

	byName := make(map[string]nameInfo)
	for _, n := range names {
		byName[n.Name] = n
	}

	// Apex carries SOA + NS + the synthesized RRSIG bit. RRSIG is
	// always added because we sign at every owner with records.
	apex, ok := byName["example.test."]
	if !ok {
		t.Fatal("apex missing from parsed names")
	}
	for _, want := range []uint16{dns.TypeSOA, dns.TypeNS, dns.TypeRRSIG} {
		if !containsType(apex.Types, want) {
			t.Errorf("apex missing type %s", dns.TypeToString[want])
		}
	}

	// `host` has both A and AAAA — exercise multi-type bitmap.
	host, ok := byName["host.example.test."]
	if !ok {
		t.Fatal("host.example.test. missing")
	}
	for _, want := range []uint16{dns.TypeA, dns.TypeAAAA, dns.TypeRRSIG} {
		if !containsType(host.Types, want) {
			t.Errorf("host missing type %s", dns.TypeToString[want])
		}
	}
}

func TestParseZoneFileOptOutDetection(t *testing.T) {
	path := writeZone(t, flatZone)
	names, _, err := parseZoneFile(path)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]nameInfo)
	for _, n := range names {
		byName[n.Name] = n
	}

	// Insecure delegation: NS but no DS, not at the apex → opted out.
	if !byName["insecure.example.test."].OptedOut {
		t.Error("insecure.example.test. should be OptedOut (NS without DS)")
	}
	// Secure delegation: NS + DS → not opted out.
	if byName["secure.example.test."].OptedOut {
		t.Error("secure.example.test. should NOT be OptedOut (has DS)")
	}
	// Apex has NS but is the apex itself, not a delegation → not opted out.
	if byName["example.test."].OptedOut {
		t.Error("apex must not be flagged as an insecure delegation")
	}
	// Plain A-record host: not a delegation at all.
	if byName["host.example.test."].OptedOut {
		t.Error("host.example.test. should NOT be OptedOut (no NS)")
	}
}

func TestParseZoneFileMissingSOA(t *testing.T) {
	// A zone file without an SOA shouldn't pass — without an apex
	// we can't anchor the chain. The miekg/dns parser tolerates the
	// missing SOA; our code adds the explicit check.
	path := writeZone(t, `$ORIGIN example.test.
$TTL 3600
host            IN  A   10.0.0.1
`)
	_, _, err := parseZoneFile(path)
	if err == nil || !strings.Contains(err.Error(), "no SOA") {
		t.Fatalf("expected 'no SOA' error, got %v", err)
	}
}

func TestParseZoneFileMissingFile(t *testing.T) {
	_, _, err := parseZoneFile(filepath.Join(t.TempDir(), "does-not-exist.zone"))
	if err == nil || !strings.Contains(err.Error(), "open") {
		t.Fatalf("expected open error, got %v", err)
	}
}

func TestParseZoneFileIncludesDisabled(t *testing.T) {
	// $INCLUDE is a footgun for centrally-rendered zones (DCIM
	// shouldn't be reaching into the filesystem from a zone file).
	// We disable it; the parser should error on a $INCLUDE line.
	path := writeZone(t, `$ORIGIN example.test.
$TTL 3600
@               IN  SOA ns1.example.test. hostmaster.example.test. (
                        1 3600 600 86400 300 )
                IN  NS  ns1.example.test.
$INCLUDE /etc/passwd
`)
	_, _, err := parseZoneFile(path)
	if err == nil {
		t.Fatal("expected $INCLUDE to be rejected")
	}
}

func TestLoadChainNoOpWithoutZoneFile(t *testing.T) {
	n := &Nsec3Sign{}
	if err := n.loadChain(); err != nil {
		t.Fatalf("loadChain with empty ZoneFile: %v", err)
	}
	if n.Chain != nil {
		t.Fatal("Chain populated despite no ZoneFile")
	}
}

func TestLoadChainPopulates(t *testing.T) {
	n := &Nsec3Sign{
		Salt:       "aabbccdd",
		Iterations: 0,
		ZoneFile:   writeZone(t, flatZone),
	}
	if err := n.loadChain(); err != nil {
		t.Fatalf("loadChain: %v", err)
	}
	if n.Chain == nil {
		t.Fatal("Chain not populated")
	}
	if n.Chain.Apex != "example.test." {
		t.Fatalf("Chain.Apex = %s, want example.test.", n.Chain.Apex)
	}
	if n.Chain.matchingNSEC3("host.example.test.") == nil {
		t.Fatal("host.example.test. missing from chain")
	}
	if n.Chain.matchingNSEC3("missing.example.test.") != nil {
		t.Fatal("missing.example.test. should not be in chain")
	}
}

func TestServeDNSRealZoneFileEndToEnd(t *testing.T) {
	// Wire everything together: real zone file, real keys, real
	// ServeDNS path. Drives an NXDOMAIN query and verifies the
	// resulting NSEC3 records validate against the loaded public
	// key. This is the closest unit-test analogue to running
	// `dig @host -p 15353 +dnssec missing.example.test.` against
	// a CoreDNS instance with the plugin enabled.
	const zone = "example.test."
	zskDK, zskPriv := generateDNSKEY(t, zone, dns.ECDSAP256SHA256, 256, 256)
	zsk := &signingKey{KeyTag: zskDK.KeyTag(), Public: zskDK, Private: zskPriv}

	n := &Nsec3Sign{
		Zones:      []string{zone},
		Keys:       []*signingKey{zsk},
		Salt:       "aabbccdd",
		Iterations: 0,
		ZoneFile:   writeZone(t, flatZone),
		Next:       &nxdomainHandler{soa: soaFor(zone)},
	}
	if err := n.loadChain(); err != nil {
		t.Fatal(err)
	}

	w := &captureWriter{}
	if _, err := n.ServeDNS(context.Background(), w, query("missing."+zone, dns.TypeA, true)); err != nil {
		t.Fatal(err)
	}

	// Pair NSEC3 records with their RRSIGs by owner, then verify
	// each pair. A single failing Verify is a sign that the
	// chain-build → render-NSEC3 → sign pipeline is producing
	// bytes that don't match what we sign over — the failure mode
	// production would hit.
	byOwner := make(map[string][]dns.RR)
	sigByOwner := make(map[string]*dns.RRSIG)
	for _, rr := range w.captured.Ns {
		switch v := rr.(type) {
		case *dns.NSEC3:
			byOwner[v.Hdr.Name] = append(byOwner[v.Hdr.Name], v)
		case *dns.RRSIG:
			if v.TypeCovered == dns.TypeNSEC3 {
				sigByOwner[v.Hdr.Name] = v
			}
		}
	}
	if len(byOwner) == 0 {
		t.Fatal("no NSEC3 records in authority section")
	}
	for owner, rrs := range byOwner {
		sig, ok := sigByOwner[owner]
		if !ok {
			t.Fatalf("NSEC3 at %s has no RRSIG", owner)
		}
		if err := sig.Verify(zskDK, rrs); err != nil {
			t.Fatalf("RRSIG.Verify at %s: %v", owner, err)
		}
	}
}
