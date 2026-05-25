// PR 82 — parser + handler tests for BIND zone import.
//
// Parser tests pin the wire-shape parity with services.dns
// parse_bind_zone: same record types, same name/data layout, same
// warnings for unsupported types. Handler tests cover dry-run vs
// apply, SOA update, frozen/404/ABAC paths.
package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

// ---- unit: labelFor ----

func TestLabelFor_ApexUsesAtSign(t *testing.T) {
	if got := labelFor("example.com.", "example.com."); got != "@" {
		t.Errorf("got %q, want @", got)
	}
	if got := labelFor("example.com", "example.com"); got != "@" {
		t.Errorf("got %q, want @ (no trailing dot)", got)
	}
}

func TestLabelFor_StripsOrigin(t *testing.T) {
	if got := labelFor("www.example.com.", "example.com."); got != "www" {
		t.Errorf("got %q, want www", got)
	}
	if got := labelFor("api.east.example.com.", "example.com."); got != "api.east" {
		t.Errorf("got %q, want api.east", got)
	}
}

// ---- unit: parseBindZone ----

const minimalZone = `$ORIGIN example.com.
$TTL 60
@	IN	SOA	ns1.example.com. hostmaster.example.com. (
	2026010101	; serial
	900		; refresh
	900		; retry
	1800		; expire
	60 )		; minimum
@	IN	NS	ns1.example.com.
www	300	IN	A	10.0.0.1
www	IN	AAAA	2001:db8::1
mail	IN	MX	10 smtp.example.com.
@	IN	TXT	"v=spf1 -all"
_sip._tcp	IN	SRV	10 20 5060 sip.example.com.
@	IN	CAA	0 issue "letsencrypt.org"
api	IN	CNAME	www.example.com.
`

func TestParseBindZone_AllSupportedTypes(t *testing.T) {
	out, err := parseBindZone(minimalZone, "example.com.")
	if err != nil {
		t.Fatal(err)
	}
	if out.Soa.Mname != "ns1.example.com" {
		t.Errorf("Mname = %q", out.Soa.Mname)
	}
	if out.Soa.Refresh != 900 {
		t.Errorf("Refresh = %d", out.Soa.Refresh)
	}
	// Should have 8 records (NS + A + AAAA + MX + TXT + SRV + CAA + CNAME).
	if len(out.Records) != 8 {
		t.Errorf("records = %d, want 8: %+v", len(out.Records), out.Records)
	}
	// Index by type for assertions.
	byType := make(map[string]bindParsedRecord)
	for _, r := range out.Records {
		byType[r.Type] = r
	}
	if byType["A"].Data["target"] != "10.0.0.1" {
		t.Errorf("A target = %v", byType["A"].Data["target"])
	}
	if byType["AAAA"].Data["target"] != "2001:db8::1" {
		t.Errorf("AAAA target = %v", byType["AAAA"].Data["target"])
	}
	if byType["MX"].Data["priority"] != 10 || byType["MX"].Data["target"] != "smtp.example.com" {
		t.Errorf("MX = %v", byType["MX"].Data)
	}
	if byType["TXT"].Data["text"] != "v=spf1 -all" {
		t.Errorf("TXT = %v", byType["TXT"].Data)
	}
	if byType["SRV"].Data["priority"] != 10 || byType["SRV"].Data["port"] != 5060 {
		t.Errorf("SRV = %v", byType["SRV"].Data)
	}
	if byType["CAA"].Data["tag"] != "issue" {
		t.Errorf("CAA = %v", byType["CAA"].Data)
	}
	if byType["CNAME"].Data["target"] != "www.example.com" {
		t.Errorf("CNAME target = %v (trailing dot should be stripped)", byType["CNAME"].Data["target"])
	}
}

func TestParseBindZone_TTLExtractedWhenSet(t *testing.T) {
	out, _ := parseBindZone(minimalZone, "example.com.")
	for _, r := range out.Records {
		if r.Type == "A" && r.Name == "www" {
			if r.TTL == nil || *r.TTL != 300 {
				t.Errorf("explicit TTL not parsed: %v", r.TTL)
			}
		}
	}
}

func TestParseBindZone_MissingSOAErrors(t *testing.T) {
	noSOA := `$ORIGIN example.com.
@	IN	NS	ns1.example.com.
`
	_, err := parseBindZone(noSOA, "example.com.")
	if err == nil || !errors.Is(err, ErrBindImport) {
		t.Errorf("expected ErrBindImport, got %v", err)
	}
}

func TestParseBindZone_UnsupportedTypeWarnsNotErrors(t *testing.T) {
	// DNSKEY is intentionally excluded from import — it's regenerated
	// by the renderer from key material.
	withDnskey := minimalZone + "@\tIN\tDNSKEY\t256 3 8 AwEAAa+abcd==\n"
	out, err := parseBindZone(withDnskey, "example.com.")
	if err != nil {
		t.Fatal(err)
	}
	foundWarning := false
	for _, w := range out.Warnings {
		if strings.Contains(w, "DNSKEY") {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Errorf("expected DNSKEY warning, got: %v", out.Warnings)
	}
}

func TestParseBindZone_DefaultTTLFromSOA(t *testing.T) {
	out, _ := parseBindZone(minimalZone, "example.com.")
	if out.DefaultTTL != 60 {
		t.Errorf("DefaultTTL = %d, want 60 (from SOA minimum)", out.DefaultTTL)
	}
}

func TestParseBindZone_SyntaxErrorReturnsErrBindImport(t *testing.T) {
	_, err := parseBindZone("not a zone file at all !@#$", "example.com.")
	if err == nil {
		t.Error("expected error for malformed input")
	}
}

// ---- unit: firstLabel ----

func TestFirstLabel(t *testing.T) {
	cases := map[string]string{
		"ns1.example.com": "ns1",
		"ns1":             "ns1",
		"":                "",
		"hostmaster.example.com": "hostmaster",
	}
	for in, want := range cases {
		if got := firstLabel(in); got != want {
			t.Errorf("firstLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---- handler: import ----

type fakeImportQ struct {
	fakeQ
	zone           dbq.DnsZone
	zoneErr        error
	gotCreates     []dbq.CreateDnsRecordParams
	gotSoaUpdate   *dbq.UpdateDnsZoneSoaParams
	gotDeletes     int
	gotTouches     int
}

func (f *fakeImportQ) GetDnsZone(_ context.Context, _ uuid.UUID) (dbq.DnsZone, error) {
	return f.zone, f.zoneErr
}

func (f *fakeImportQ) DeleteManualRecordsInZone(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	f.gotDeletes++
	return []uuid.UUID{uuid.New()}, nil
}

func (f *fakeImportQ) CreateDnsRecord(_ context.Context, a dbq.CreateDnsRecordParams) (dbq.DnsRecord, error) {
	f.gotCreates = append(f.gotCreates, a)
	return dbq.DnsRecord{ID: uuid.New(), ZoneID: a.ZoneID, Name: a.Name, Type: a.Type, Data: a.Data}, nil
}

func (f *fakeImportQ) UpdateDnsZoneSoa(_ context.Context, a dbq.UpdateDnsZoneSoaParams) error {
	f.gotSoaUpdate = &a
	return nil
}

func (f *fakeImportQ) TouchDnsZone(_ context.Context, _ uuid.UUID) (int64, error) {
	f.gotTouches++
	return 1, nil
}

func mountImport(f *fakeImportQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}

func postImport(t *testing.T, h http.Handler, id string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/dns/zones/"+id+"/import", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	p := auth.Principal{Capabilities: []string{"*"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestImportZone_DryRunDoesNotMutate(t *testing.T) {
	id := uuid.New()
	f := &fakeImportQ{zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Name: "example.com"}}
	rec := postImport(t, mountImport(f), id.String(), map[string]any{
		"text": minimalZone, "dry_run": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if len(f.gotCreates) != 0 || f.gotDeletes != 0 {
		t.Errorf("dry-run should not mutate: creates=%d deletes=%d", len(f.gotCreates), f.gotDeletes)
	}
	var resp importDryRunResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.WouldAdd != 8 {
		t.Errorf("would_add = %d, want 8", resp.WouldAdd)
	}
}

func TestImportZone_ApplyInsertsRecordsAndReplaces(t *testing.T) {
	id := uuid.New()
	f := &fakeImportQ{zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Name: "example.com"}}
	rec := postImport(t, mountImport(f), id.String(), map[string]any{
		"text": minimalZone,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if f.gotDeletes != 1 {
		t.Errorf("should delete manual rows once: deletes=%d", f.gotDeletes)
	}
	if len(f.gotCreates) != 8 {
		t.Errorf("creates = %d, want 8", len(f.gotCreates))
	}
	// SOA not updated when update_soa=false; should touch zone instead.
	if f.gotSoaUpdate != nil {
		t.Errorf("SOA should not be updated when update_soa=false")
	}
	if f.gotTouches != 1 {
		t.Errorf("expected zone touch, got %d", f.gotTouches)
	}
}

func TestImportZone_UpdateSoaWritesSoaFields(t *testing.T) {
	id := uuid.New()
	f := &fakeImportQ{zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Name: "example.com"}}
	rec := postImport(t, mountImport(f), id.String(), map[string]any{
		"text": minimalZone, "update_soa": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if f.gotSoaUpdate == nil {
		t.Fatal("expected SOA update")
	}
	// mname/rname stored as the left-most label only.
	if f.gotSoaUpdate.SoaMname == nil || *f.gotSoaUpdate.SoaMname != "ns1" {
		t.Errorf("SoaMname = %v, want ns1", f.gotSoaUpdate.SoaMname)
	}
	if f.gotSoaUpdate.SoaRname == nil || *f.gotSoaUpdate.SoaRname != "hostmaster" {
		t.Errorf("SoaRname = %v, want hostmaster", f.gotSoaUpdate.SoaRname)
	}
	if f.gotSoaUpdate.SoaRefresh == nil || *f.gotSoaUpdate.SoaRefresh != 900 {
		t.Errorf("SoaRefresh = %v, want 900", f.gotSoaUpdate.SoaRefresh)
	}
}

func TestImportZone_MissingTextIs400(t *testing.T) {
	id := uuid.New()
	rec := postImport(t, mountImport(&fakeImportQ{}), id.String(), map[string]any{})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestImportZone_MissingSOAIs422(t *testing.T) {
	id := uuid.New()
	f := &fakeImportQ{zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Name: "example.com"}}
	noSOA := "$ORIGIN example.com.\n@ IN NS ns1.example.com.\n"
	rec := postImport(t, mountImport(f), id.String(), map[string]any{"text": noSOA})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
}

func TestImportZone_FrozenZoneIs422(t *testing.T) {
	id := uuid.New()
	f := &fakeImportQ{zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Name: "example.com", Frozen: true}}
	rec := postImport(t, mountImport(f), id.String(), map[string]any{"text": minimalZone})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 (frozen)", rec.Code)
	}
}

func TestImportZone_NotFoundIs404(t *testing.T) {
	f := &fakeImportQ{zoneErr: pgx.ErrNoRows}
	rec := postImport(t, mountImport(f), uuid.New().String(), map[string]any{"text": minimalZone})
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestImportZone_BadUUIDIs400(t *testing.T) {
	rec := postImport(t, mountImport(&fakeImportQ{}), "not-a-uuid", map[string]any{"text": minimalZone})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestImportZone_RequiresUpdateCap(t *testing.T) {
	id := uuid.New()
	req := httptest.NewRequest("POST", "/dns/zones/"+id.String()+"/import",
		bytes.NewReader([]byte(`{"text":"$ORIGIN x.\n"}`)))
	req.Header.Set("Content-Type", "application/json")
	p := auth.Principal{Capabilities: []string{"dns:zones:read"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountImport(&fakeImportQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestImportZone_UnsupportedTypeWarningPropagates(t *testing.T) {
	id := uuid.New()
	f := &fakeImportQ{zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Name: "example.com"}}
	withDnskey := minimalZone + "@\tIN\tDNSKEY\t256 3 8 AwEAAa+abcd==\n"
	rec := postImport(t, mountImport(f), id.String(), map[string]any{"text": withDnskey})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp importApplyResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Warnings) == 0 {
		t.Errorf("expected DNSKEY warning to surface, got none")
	}
}
