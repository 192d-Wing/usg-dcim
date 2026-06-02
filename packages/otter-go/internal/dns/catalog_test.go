package dns

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// fixedID returns a deterministic UUID so RenderCatalogZone tests
// can assert exact byte output. Hex form (no dashes) is what Python's
// `uuid.UUID.hex` emits and what the renderer must use for labels.
func fixedID(b byte) uuid.UUID {
	var u uuid.UUID
	for i := range u {
		u[i] = b
	}
	return u
}

func mkMember(name, kind string, ts time.Time, idByte byte) dbq.DnsZone {
	return dbq.DnsZone{
		ID:        fixedID(idByte),
		Name:      name,
		Kind:      kind,
		UpdatedAt: ts,
	}
}

func TestRenderCatalogZone_Empty(t *testing.T) {
	out := RenderCatalogZone("catalog.example.", nil, 0, 0, nil)
	if !strings.HasPrefix(out, "$ORIGIN catalog.example.\n") {
		t.Errorf("apex prefix wrong: %s", out)
	}
	if !strings.Contains(out, "$TTL 3600\n") {
		t.Errorf("default TTL should be 3600 when 0 passed; got: %s", out)
	}
	// Empty members → serial defaults to 1 (Python's `or 1` short-circuit).
	if !strings.Contains(out, "\t1\t; serial\n") {
		t.Errorf("empty members serial should be 1; got: %s", out)
	}
	// Version schema RR mandated by RFC 9432.
	if !strings.Contains(out, "version\tIN\tTXT\t\"2\"\n") {
		t.Errorf("missing version TXT record: %s", out)
	}
	// No per-member properties section when there are no members.
	if strings.Contains(out, "per-member properties") {
		t.Errorf("empty catalog should NOT emit properties section: %s", out)
	}
}

func TestRenderCatalogZone_TrailingDotNormalized(t *testing.T) {
	// Caller passes apex without trailing dot — renderer adds it.
	out := RenderCatalogZone("catalog.example", nil, 0, 0, nil)
	if !strings.Contains(out, "$ORIGIN catalog.example.\n") {
		t.Errorf("apex not dot-normalized: %s", out)
	}
}

func TestRenderCatalogZone_AutoSerialFromMaxEpoch(t *testing.T) {
	ts1 := time.Unix(1700000000, 0).UTC()
	ts2 := time.Unix(1800000000, 0).UTC()
	members := []dbq.DnsZone{
		mkMember("zone-a.example.", "apex", ts1, 0x01),
		mkMember("zone-b.example.", "apex", ts2, 0x02),
	}
	out := RenderCatalogZone("c.example.", members, 0, 0, nil)
	// Serial == max(epoch) → 1800000000.
	if !strings.Contains(out, "\t1800000000\t; serial\n") {
		t.Errorf("auto-serial should be max(epoch); got: %s", out)
	}
}

func TestRenderCatalogZone_ExplicitSerialWins(t *testing.T) {
	ts := time.Unix(1700000000, 0).UTC()
	members := []dbq.DnsZone{mkMember("z.example.", "apex", ts, 0x01)}
	out := RenderCatalogZone("c.example.", members, 0, 42, nil)
	if !strings.Contains(out, "\t42\t; serial\n") {
		t.Errorf("explicit serial should win over auto; got: %s", out)
	}
}

func TestRenderCatalogZone_MembersSortedByName(t *testing.T) {
	ts := time.Unix(1700000000, 0).UTC()
	// Insert in reverse order; renderer must sort by lower-cased name.
	members := []dbq.DnsZone{
		mkMember("Charlie.example.", "site", ts, 0x03),
		mkMember("alpha.example.", "apex", ts, 0x01),
		mkMember("BRAVO.example.", "site", ts, 0x02),
	}
	out := RenderCatalogZone("c.example.", members, 0, 100, nil)
	// PTR ordering matches lower-case sort: alpha, BRAVO, Charlie.
	idxA := strings.Index(out, ".zones\tIN\tPTR\talpha.example.")
	idxB := strings.Index(out, ".zones\tIN\tPTR\tBRAVO.example.")
	idxC := strings.Index(out, ".zones\tIN\tPTR\tCharlie.example.")
	if idxA < 0 || idxB < 0 || idxC < 0 {
		t.Fatalf("missing PTRs; got: %s", out)
	}
	if !(idxA < idxB && idxB < idxC) {
		t.Errorf("PTR order wrong: alpha=%d bravo=%d charlie=%d", idxA, idxB, idxC)
	}
}

func TestRenderCatalogZone_PerMemberPTRGroupEpoch(t *testing.T) {
	ts := time.Unix(1700000000, 0).UTC()
	members := []dbq.DnsZone{mkMember("zone-a.example.", "site", ts, 0x01)}
	out := RenderCatalogZone("c.example.", members, 0, 1, nil)
	// PTR label uses .hex (no dashes) for Python parity.
	hexID := "01010101010101010101010101010101"
	want := []string{
		hexID + ".zones\tIN\tPTR\tzone-a.example.",
		"group." + hexID + ".zones\tIN\tTXT\t\"site\"",
		"epoch." + hexID + ".zones\tIN\tTXT\t\"1700000000\"",
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
}

func TestRenderCatalogZone_PrimariesIPv4AndIPv6(t *testing.T) {
	ts := time.Unix(1700000000, 0).UTC()
	members := []dbq.DnsZone{mkMember("z.example.", "apex", ts, 0x01)}
	out := RenderCatalogZone("c.example.", members, 0, 1,
		[]string{"10.0.0.1/24", "2001:db8::1"})
	hexID := "01010101010101010101010101010101"
	// IPv4 → A, /24 stripped before parsing.
	if !strings.Contains(out, "primaries."+hexID+".zones\tIN\tA\t10.0.0.1") {
		t.Errorf("missing IPv4 primary A record: %s", out)
	}
	// IPv6 → AAAA.
	if !strings.Contains(out, "primaries."+hexID+".zones\tIN\tAAAA\t2001:db8::1") {
		t.Errorf("missing IPv6 primary AAAA record: %s", out)
	}
}

func TestRenderCatalogZone_PrimariesBadAddressDropped(t *testing.T) {
	ts := time.Unix(1700000000, 0).UTC()
	members := []dbq.DnsZone{mkMember("z.example.", "apex", ts, 0x01)}
	out := RenderCatalogZone("c.example.", members, 0, 1,
		[]string{"not-an-ip", "10.0.0.1"})
	hexID := "01010101010101010101010101010101"
	// "not-an-ip" is silently dropped (matches Python's
	// `except ValueError: continue`); the valid one remains.
	if strings.Contains(out, "not-an-ip") {
		t.Errorf("malformed primary should be dropped: %s", out)
	}
	if !strings.Contains(out, "primaries."+hexID+".zones\tIN\tA\t10.0.0.1") {
		t.Errorf("valid primary should remain: %s", out)
	}
}

func TestRenderCatalogZone_DeterministicEtag(t *testing.T) {
	// Same input → same output across calls. Critical for the
	// bundle etag stability the cutover relies on.
	ts := time.Unix(1700000000, 0).UTC()
	members := []dbq.DnsZone{
		mkMember("b.example.", "site", ts, 0x02),
		mkMember("a.example.", "apex", ts, 0x01),
	}
	out1 := RenderCatalogZone("c.example.", members, 0, 1, []string{"10.0.0.1"})
	out2 := RenderCatalogZone("c.example.", members, 0, 1, []string{"10.0.0.1"})
	if out1 != out2 {
		t.Error("two calls with identical input produced different output")
	}
	// Same set, different input order → still identical (the sort
	// is what makes the etag stable across DB rebuilds).
	members2 := []dbq.DnsZone{
		mkMember("a.example.", "apex", ts, 0x01),
		mkMember("b.example.", "site", ts, 0x02),
	}
	out3 := RenderCatalogZone("c.example.", members2, 0, 1, []string{"10.0.0.1"})
	if out1 != out3 {
		t.Errorf("input order changed output:\nout1=%q\nout3=%q", out1, out3)
	}
}

// Python defaults defaultTTL=3600 — when the caller passes a custom
// TTL it must flow into both $TTL and the SOA minimum field.
func TestRenderCatalogZone_CustomTTL(t *testing.T) {
	out := RenderCatalogZone("c.example.", nil, 1800, 1, nil)
	if !strings.Contains(out, "$TTL 1800\n") {
		t.Errorf("custom $TTL not threaded: %s", out)
	}
	if !strings.Contains(out, "\t1800)\t; minimum\n") {
		t.Errorf("custom SOA minimum not threaded: %s", out)
	}
}

// IPv4-mapped IPv6 (`::ffff:1.2.3.4`) is technically an IPv4 address
// in 16-byte form. net.ParseIP returns non-nil and ip.To4() also
// returns non-nil — naive code would emit an A record. Python's
// ipaddress.ip_address keeps version=6 and emits AAAA with the
// `::ffff:1.2.3.4` form. Mirror Python to preserve byte-for-byte
// etag parity.
func TestRenderCatalogZone_IPv4MappedIPv6IsAAAA(t *testing.T) {
	ts := time.Unix(1700000000, 0).UTC()
	members := []dbq.DnsZone{mkMember("z.example.", "apex", ts, 0x01)}
	out := RenderCatalogZone("c.example.", members, 0, 1, []string{"::ffff:1.2.3.4"})
	if !strings.Contains(out, "IN\tAAAA\t::ffff:1.2.3.4") {
		t.Errorf("IPv4-mapped IPv6 must render as AAAA preserving input form; got: %s", out)
	}
	if strings.Contains(out, "IN\tA\t1.2.3.4") {
		t.Errorf("must NOT collapse ::ffff:1.2.3.4 to a v4-only A record: %s", out)
	}
}

// Members with a zero updated_at must not emit garbage epoch
// (Go's zero time.Unix() = -62135596800). The guard emits 0 so
// downstream consumers can still parse the bundle and the
// problem surfaces in the TXT value rather than as a BIND syntax
// error.
func TestRenderCatalogZone_ZeroTimeEpochGuard(t *testing.T) {
	members := []dbq.DnsZone{
		{ID: fixedID(0x01), Name: "z.example.", Kind: "apex"}, // UpdatedAt left as zero
	}
	out := RenderCatalogZone("c.example.", members, 0, 1, nil)
	hexID := "01010101010101010101010101010101"
	want := "epoch." + hexID + ".zones\tIN\tTXT\t\"0\""
	if !strings.Contains(out, want) {
		t.Errorf("zero-time epoch should render as 0, not garbage; got: %s", out)
	}
	if strings.Contains(out, "-62135596800") {
		t.Errorf("emitted Go-zero-time Unix value into bundle: %s", out)
	}
}

// Golden-byte test — locks the entire output for a known fixture so
// any future formatting drift fails loudly. The expected string was
// computed by tracing Python's render_catalog_zone for the same
// inputs and asserts byte-for-byte parity (the etag promise of this
// renderer). A failure here means cross-language drift.
//
// This is the canonical anti-regression test for the bundle work —
// any future renderer tweak that bumps the etag silently is caught
// here.
func TestRenderCatalogZone_GoldenByteOutput(t *testing.T) {
	ts := time.Unix(1700000000, 0).UTC()
	members := []dbq.DnsZone{
		{ID: fixedID(0x01), Name: "alpha.example.", Kind: "apex", UpdatedAt: ts},
		{ID: fixedID(0x02), Name: "beta.example.", Kind: "site", UpdatedAt: ts},
	}
	got := RenderCatalogZone("catalog.example.", members, 3600, 42,
		[]string{"10.0.0.1"})
	hexA := "01010101010101010101010101010101"
	hexB := "02020202020202020202020202020202"
	want := "$ORIGIN catalog.example.\n" +
		"$TTL 3600\n" +
		"@\tIN\tSOA\tinvalid. hostmaster.catalog.example. (\n" +
		"\t\t\t42\t; serial\n" +
		"\t\t\t3600\t; refresh\n" +
		"\t\t\t600\t; retry\n" +
		"\t\t\t604800\t; expire\n" +
		"\t\t\t3600)\t; minimum\n" +
		"@\tIN\tNS\tinvalid.\n" +
		"version\tIN\tTXT\t\"2\"\n" +
		"\n" +
		hexA + ".zones\tIN\tPTR\talpha.example.\n" +
		hexB + ".zones\tIN\tPTR\tbeta.example.\n" +
		"\n" +
		"; --- per-member properties (RFC 9432 §5) ---\n" +
		"group." + hexA + ".zones\tIN\tTXT\t\"apex\"\n" +
		"epoch." + hexA + ".zones\tIN\tTXT\t\"1700000000\"\n" +
		"primaries." + hexA + ".zones\tIN\tA\t10.0.0.1\n" +
		"group." + hexB + ".zones\tIN\tTXT\t\"site\"\n" +
		"epoch." + hexB + ".zones\tIN\tTXT\t\"1700000000\"\n" +
		"primaries." + hexB + ".zones\tIN\tA\t10.0.0.1\n"
	if got != want {
		t.Errorf("golden mismatch\n--- want ---\n%q\n--- got ---\n%q", want, got)
	}
}

// Empty-members golden — the trailing-newline behavior differs from
// non-empty so this needs its own pin.
func TestRenderCatalogZone_EmptyGolden(t *testing.T) {
	got := RenderCatalogZone("catalog.example.", nil, 3600, 1, nil)
	want := "$ORIGIN catalog.example.\n" +
		"$TTL 3600\n" +
		"@\tIN\tSOA\tinvalid. hostmaster.catalog.example. (\n" +
		"\t\t\t1\t; serial\n" +
		"\t\t\t3600\t; refresh\n" +
		"\t\t\t600\t; retry\n" +
		"\t\t\t604800\t; expire\n" +
		"\t\t\t3600)\t; minimum\n" +
		"@\tIN\tNS\tinvalid.\n" +
		"version\tIN\tTXT\t\"2\"\n" +
		"\n"
	if got != want {
		t.Errorf("empty-catalog golden mismatch\n--- want ---\n%q\n--- got ---\n%q", want, got)
	}
}
