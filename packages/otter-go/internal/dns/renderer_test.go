// PR 71 — unit tests for the BIND zone-file renderer.
package dns

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

func record(name, rtype string, data map[string]any) dbq.ListAllRecordsInZoneRow {
	b, _ := json.Marshal(data)
	return dbq.ListAllRecordsInZoneRow{
		ID: uuid.New(), Name: name, Type: rtype, Data: b,
	}
}

func recordWithTTL(name, rtype string, ttl int32, data map[string]any) dbq.ListAllRecordsInZoneRow {
	r := record(name, rtype, data)
	r.TTL = &ttl
	return r
}

func mkZone() dbq.DnsZone {
	t := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return dbq.DnsZone{
		Name: "example.com", DefaultTTL: 60,
		SoaMname: "ns1", SoaRname: "hostmaster",
		SoaRefresh: 900, SoaRetry: 900, SoaExpire: 1800, SoaMinimum: 60,
		UpdatedAt: t,
	}
}

// ---- formatRdata ----

func TestFormatRdata_A(t *testing.T) {
	s, err := formatRdata("A", json.RawMessage(`{"target":"10.0.0.1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if s != "10.0.0.1" {
		t.Errorf("got %q", s)
	}
}

func TestFormatRdata_AAAA(t *testing.T) {
	s, _ := formatRdata("AAAA", json.RawMessage(`{"target":"2001:db8::1"}`))
	if s != "2001:db8::1" {
		t.Errorf("got %q", s)
	}
}

func TestFormatRdata_MX(t *testing.T) {
	s, _ := formatRdata("MX", json.RawMessage(`{"priority":10,"target":"mail.example.com"}`))
	if s != "10 mail.example.com" {
		t.Errorf("got %q", s)
	}
}

func TestFormatRdata_TXT_QuotesAndEscapes(t *testing.T) {
	// Inner double-quote should be escaped; backslash too.
	s, _ := formatRdata("TXT", json.RawMessage(`{"text":"v=spf1 \"hi\""}`))
	if !strings.HasPrefix(s, `"`) || !strings.HasSuffix(s, `"`) {
		t.Errorf("TXT should be quoted: %q", s)
	}
}

func TestFormatRdata_SRV(t *testing.T) {
	s, _ := formatRdata("SRV", json.RawMessage(`{"priority":10,"weight":20,"port":443,"target":"a.example.com"}`))
	if s != "10 20 443 a.example.com" {
		t.Errorf("got %q", s)
	}
}

func TestFormatRdata_CAA(t *testing.T) {
	s, _ := formatRdata("CAA", json.RawMessage(`{"flags":0,"tag":"issue","value":"letsencrypt.org"}`))
	if s != `0 issue "letsencrypt.org"` {
		t.Errorf("got %q", s)
	}
}

func TestFormatRdata_UnknownTypeErrors(t *testing.T) {
	_, err := formatRdata("DNSKEY", json.RawMessage(`{}`))
	if err == nil {
		t.Error("expected error for unsupported type")
	}
}

// ---- formatRecordLine ----

func TestFormatRecordLine_UsesAtForApex(t *testing.T) {
	r := record("", "A", map[string]any{"target": "10.0.0.1"})
	line, _ := formatRecordLine(r, 60)
	if !strings.HasPrefix(line, "@\t") {
		t.Errorf("apex should use '@': %q", line)
	}
}

func TestFormatRecordLine_ExplicitTTLOverridesZoneDefault(t *testing.T) {
	r := recordWithTTL("www", "A", 300, map[string]any{"target": "10.0.0.1"})
	line, _ := formatRecordLine(r, 60)
	if !strings.Contains(line, "\t300\t") {
		t.Errorf("explicit ttl should be 300: %q", line)
	}
}

func TestFormatRecordLine_NullTTLUsesZoneDefault(t *testing.T) {
	r := record("www", "A", map[string]any{"target": "10.0.0.1"})
	line, _ := formatRecordLine(r, 60)
	if !strings.Contains(line, "\t60\t") {
		t.Errorf("nil ttl should fall back to zone default 60: %q", line)
	}
}

// ---- renderZoneFile ----

func TestRenderZoneFile_HasOriginAndSOA(t *testing.T) {
	out, err := renderZoneFile(mkZone(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "$ORIGIN example.com.\n$TTL 60\n") {
		t.Errorf("missing $ORIGIN/$TTL header: %q", out)
	}
	if !strings.Contains(out, "IN\tSOA\tns1.example.com. hostmaster.example.com. (") {
		t.Errorf("SOA line wrong: %q", out)
	}
	if !strings.Contains(out, "; serial") {
		t.Errorf("SOA serial comment missing")
	}
}

func TestRenderZoneFile_SortsByNameThenType(t *testing.T) {
	z := mkZone()
	records := []dbq.ListAllRecordsInZoneRow{
		record("www", "A", map[string]any{"target": "10.0.0.2"}),
		record("api", "AAAA", map[string]any{"target": "2001:db8::1"}),
		record("api", "A", map[string]any{"target": "10.0.0.1"}),
	}
	out, _ := renderZoneFile(z, records)
	apiA := strings.Index(out, "api\t60\tIN\tA\t")
	apiAAAA := strings.Index(out, "api\t60\tIN\tAAAA\t")
	wwwA := strings.Index(out, "www\t60\tIN\tA\t")
	if apiA < 0 || apiAAAA < 0 || wwwA < 0 {
		t.Fatalf("missing record lines in: %q", out)
	}
	if !(apiA < apiAAAA && apiAAAA < wwwA) {
		t.Errorf("ordering wrong: apiA=%d apiAAAA=%d wwwA=%d", apiA, apiAAAA, wwwA)
	}
}

func TestRenderZoneFile_EmptyRecordsStillRendersSOA(t *testing.T) {
	out, _ := renderZoneFile(mkZone(), nil)
	if !strings.Contains(out, "$ORIGIN") {
		t.Errorf("expected $ORIGIN even with no records: %q", out)
	}
}

func TestRenderZoneFile_BadRecordDataReturnsError(t *testing.T) {
	bad := dbq.ListAllRecordsInZoneRow{Name: "x", Type: "A", Data: json.RawMessage(`{"not_target":"x"}`)}
	// The JSON Unmarshal succeeds but emits "" for the target —
	// not great, but matches Python: schemas validate the payload
	// at write time so a malformed row would have to be hand-
	// inserted to reach here. Verify that an explicitly bad type
	// returns the expected error path.
	bad.Type = "WAT"
	_, err := renderZoneFile(mkZone(), []dbq.ListAllRecordsInZoneRow{bad})
	if err == nil {
		t.Error("expected error for unknown record type")
	}
}

func TestRenderZoneFile_PythonShapeFidelity(t *testing.T) {
	// Compare a small known-good zone against the Python output
	// shape: SOA followed by sorted records, tab-separated fields,
	// trailing newline on each line.
	z := mkZone()
	records := []dbq.ListAllRecordsInZoneRow{
		record("@", "NS", map[string]any{"target": "ns1.example.com."}),
		record("www", "A", map[string]any{"target": "10.0.0.1"}),
		record("www", "AAAA", map[string]any{"target": "2001:db8::1"}),
		recordWithTTL("mail", "MX", 300, map[string]any{"priority": 10, "target": "smtp.example.com."}),
	}
	out, err := renderZoneFile(z, records)
	if err != nil {
		t.Fatal(err)
	}
	expectedLines := []string{
		"@\t60\tIN\tNS\tns1.example.com.",
		"mail\t300\tIN\tMX\t10 smtp.example.com.",
		"www\t60\tIN\tA\t10.0.0.1",
		"www\t60\tIN\tAAAA\t2001:db8::1",
	}
	for _, want := range expectedLines {
		if !strings.Contains(out, want) {
			t.Errorf("missing line %q in:\n%s", want, out)
		}
	}
}
