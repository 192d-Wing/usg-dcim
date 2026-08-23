// PR 83 — unit + handler tests for sync-from-ipam.
//
// Pure-function tests pin the reverse-zone / PTR-owner math
// against fixed inputs. Handler tests cover the orchestrator
// flow with stubbed query responses.
package dns

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

// ---- unit: reverseZoneName ----

func TestReverseZoneName_V4(t *testing.T) {
	got, err := reverseZoneName("10.0.0.5")
	if err != nil {
		t.Fatal(err)
	}
	if got != "0.0.10.in-addr.arpa" {
		t.Errorf("got %q, want 0.0.10.in-addr.arpa", got)
	}
}

func TestReverseZoneName_V6(t *testing.T) {
	// /64 = first 16 nibbles reversed. 2001:db8::1 → first 64
	// bits = 2001:0db8:0000:0000.
	got, err := reverseZoneName("2001:db8::1")
	if err != nil {
		t.Fatal(err)
	}
	want := "0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestReverseZoneName_BadAddrErrors(t *testing.T) {
	if _, err := reverseZoneName("not-an-ip"); err == nil {
		t.Error("expected error for malformed address")
	}
}

// ---- unit: ptrOwner ----

func TestPtrOwner_V4(t *testing.T) {
	got, err := ptrOwner("10.0.0.5")
	if err != nil {
		t.Fatal(err)
	}
	if got != "5.0.0.10.in-addr.arpa." {
		t.Errorf("got %q", got)
	}
}

func TestPtrOwner_V6(t *testing.T) {
	got, err := ptrOwner("2001:db8::1")
	if err != nil {
		t.Fatal(err)
	}
	// Full 32-nibble reverse with .ip6.arpa. suffix.
	want := "1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa."
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// ---- unit: ptrLabelIn ----

func TestPtrLabelIn(t *testing.T) {
	// PTR owner = "5.0.0.10.in-addr.arpa", zone = "0.0.10.in-addr.arpa"
	// → relative label is "5".
	got := ptrLabelIn("5.0.0.10.in-addr.arpa", "0.0.10.in-addr.arpa")
	if got != "5" {
		t.Errorf("got %q, want 5", got)
	}
}

func TestPtrLabelIn_NoSuffixReturnsAsIs(t *testing.T) {
	// Caller violated the precondition — label doesn't end with
	// origin. Helper returns owner unchanged (defensive).
	got := ptrLabelIn("unrelated.example.com", "0.0.10.in-addr.arpa")
	if got != "unrelated.example.com" {
		t.Errorf("got %q", got)
	}
}

// ---- unit: forwardLabelFor ----

func TestForwardLabelFor_StripsZoneSuffix(t *testing.T) {
	got := forwardLabelFor("www.example.com", "example.com")
	if got != "www" {
		t.Errorf("got %q, want www", got)
	}
}

func TestForwardLabelFor_ApexCollapsesToAt(t *testing.T) {
	got := forwardLabelFor("example.com", "example.com")
	if got != "@" {
		t.Errorf("got %q, want @", got)
	}
}

func TestForwardLabelFor_UnrelatedReturnsAsIs(t *testing.T) {
	got := forwardLabelFor("foo.bar", "example.com")
	if got != "foo.bar" {
		t.Errorf("got %q", got)
	}
}

// ---- unit: ptrTargetFor ----

func TestPtrTargetFor_AbsoluteNameKept(t *testing.T) {
	got := ptrTargetFor("host.example.com.", "example.com")
	if got != "host.example.com." {
		t.Errorf("got %q", got)
	}
}

func TestPtrTargetFor_RelativeBecomesFQDN(t *testing.T) {
	got := ptrTargetFor("host", "example.com")
	if got != "host.example.com." {
		t.Errorf("got %q", got)
	}
}

// ---- unit: recordTypeForAddr ----

func TestRecordTypeForAddr_A(t *testing.T) {
	got, _ := recordTypeForAddr("10.0.0.5")
	if got != "A" {
		t.Errorf("got %q", got)
	}
}

func TestRecordTypeForAddr_AAAA(t *testing.T) {
	got, _ := recordTypeForAddr("2001:db8::1")
	if got != "AAAA" {
		t.Errorf("got %q", got)
	}
}

// ---- handler: sync ----

type fakeSyncQ struct {
	fakeQ
	zone        dbq.DnsZone
	zoneErr     error
	revZones    []dbq.DnsZone
	ips         []dbq.ListIPAddressesForSiteWithDnsNameRow
	dropCount   int64
	gotCreates  []dbq.CreateProjectedDnsRecordParams
	gotDelete   bool
	gotTouches  []uuid.UUID
}

func (f *fakeSyncQ) GetDnsZone(_ context.Context, _ uuid.UUID) (dbq.DnsZone, error) {
	return f.zone, f.zoneErr
}

func (f *fakeSyncQ) ListReverseZonesForSite(_ context.Context, _ dbq.ListReverseZonesForSiteParams) ([]dbq.DnsZone, error) {
	return f.revZones, nil
}

func (f *fakeSyncQ) GetReverseZoneByName(_ context.Context, _ dbq.GetReverseZoneByNameParams) (dbq.DnsZone, error) {
	return dbq.DnsZone{}, pgx.ErrNoRows
}

func (f *fakeSyncQ) CreateReverseZone(_ context.Context, a dbq.CreateReverseZoneParams) (dbq.DnsZone, error) {
	z := dbq.DnsZone{ID: uuid.New(), Name: a.Name}
	f.revZones = append(f.revZones, z)
	return z, nil
}

func (f *fakeSyncQ) ListIPAddressesForSiteWithDnsName(_ context.Context, _ uuid.UUID) ([]dbq.ListIPAddressesForSiteWithDnsNameRow, error) {
	return f.ips, nil
}

func (f *fakeSyncQ) DeleteIPAMRecordsInZones(_ context.Context, _ []uuid.UUID) error {
	f.gotDelete = true
	return nil
}

func (f *fakeSyncQ) CountIPAMRecordsInZones(_ context.Context, _ []uuid.UUID) (int64, error) {
	return f.dropCount, nil
}

func (f *fakeSyncQ) CreateProjectedDnsRecord(_ context.Context, a dbq.CreateProjectedDnsRecordParams) (uuid.UUID, error) {
	f.gotCreates = append(f.gotCreates, a)
	return uuid.New(), nil
}

func (f *fakeSyncQ) TouchDnsZone(_ context.Context, id uuid.UUID) (int64, error) {
	f.gotTouches = append(f.gotTouches, id)
	return 1, nil
}

func mountSync(f *fakeSyncQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}

func TestSyncFromIPAM_HappyPath(t *testing.T) {
	siteID := uuid.New()
	zoneID := uuid.New()
	fabricID := uuid.New()
	dnsName := "www"
	f := &fakeSyncQ{
		zone: dbq.DnsZone{ID: zoneID, FabricID: fabricID, SiteID: &siteID,
			Kind: "site", Name: "example.com"},
		ips: []dbq.ListIPAddressesForSiteWithDnsNameRow{
			{ID: uuid.New(), Address: "10.0.0.5", Source: "static", DnsName: &dnsName},
		},
		dropCount: 4,
	}
	rec := authed(t, mountSync(f), "POST", "/dns/zones/"+zoneID.String()+"/sync-from-ipam", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var resp syncResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Removed != 4 {
		t.Errorf("removed = %d, want 4", resp.Removed)
	}
	if resp.Added != 2 {
		t.Errorf("added = %d, want 2 (A + PTR)", resp.Added)
	}
	// 2 creates: A and PTR.
	if len(f.gotCreates) != 2 {
		t.Fatalf("creates = %d, want 2", len(f.gotCreates))
	}
	// First is A (forward), second is PTR (reverse).
	if f.gotCreates[0].Type != "A" || f.gotCreates[1].Type != "PTR" {
		t.Errorf("types = %s, %s; want A, PTR", f.gotCreates[0].Type, f.gotCreates[1].Type)
	}
	// Forward record should have source=ipam (static IPAM origin).
	if f.gotCreates[0].Source != "ipam" {
		t.Errorf("source = %q, want ipam", f.gotCreates[0].Source)
	}
}

func TestSyncFromIPAM_DhcpSourceBecomesDDNS(t *testing.T) {
	siteID := uuid.New()
	zoneID := uuid.New()
	dnsName := "leased-host"
	f := &fakeSyncQ{
		zone: dbq.DnsZone{ID: zoneID, FabricID: uuid.New(), SiteID: &siteID,
			Kind: "site", Name: "example.com"},
		ips: []dbq.ListIPAddressesForSiteWithDnsNameRow{
			{ID: uuid.New(), Address: "10.0.0.10", Source: "dhcp", DnsName: &dnsName},
		},
	}
	rec := authed(t, mountSync(f), "POST", "/dns/zones/"+zoneID.String()+"/sync-from-ipam", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if f.gotCreates[0].Source != "ddns" {
		t.Errorf("source = %q, want ddns for dhcp-sourced IP", f.gotCreates[0].Source)
	}
}

func TestSyncFromIPAM_AutoCreatesReverseZone(t *testing.T) {
	// No existing reverse zones → emit creates one for the IP's /24.
	siteID := uuid.New()
	zoneID := uuid.New()
	dnsName := "host1"
	f := &fakeSyncQ{
		zone: dbq.DnsZone{ID: zoneID, FabricID: uuid.New(), SiteID: &siteID,
			Kind: "site", Name: "example.com"},
		ips: []dbq.ListIPAddressesForSiteWithDnsNameRow{
			{ID: uuid.New(), Address: "10.0.0.5", Source: "static", DnsName: &dnsName},
		},
	}
	rec := authed(t, mountSync(f), "POST", "/dns/zones/"+zoneID.String()+"/sync-from-ipam", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	// revZones started empty; after create, should have one.
	foundRev := false
	for _, z := range f.revZones {
		if z.Name == "0.0.10.in-addr.arpa" {
			foundRev = true
		}
	}
	if !foundRev {
		t.Errorf("expected auto-created reverse zone, got %+v", f.revZones)
	}
}

func TestSyncFromIPAM_NonSiteZoneNoOp(t *testing.T) {
	// Apex zone (kind != site) returns 200 with zero counts. The
	// projector only runs for site zones.
	zoneID := uuid.New()
	f := &fakeSyncQ{
		zone: dbq.DnsZone{ID: zoneID, FabricID: uuid.New(), Kind: "apex"},
	}
	rec := authed(t, mountSync(f), "POST", "/dns/zones/"+zoneID.String()+"/sync-from-ipam", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(f.gotCreates) != 0 {
		t.Errorf("apex zone shouldn't create records")
	}
}

func TestSyncFromIPAM_FrozenZoneIs422(t *testing.T) {
	zoneID := uuid.New()
	f := &fakeSyncQ{
		zone: dbq.DnsZone{ID: zoneID, FabricID: uuid.New(), Frozen: true},
	}
	rec := authed(t, mountSync(f), "POST", "/dns/zones/"+zoneID.String()+"/sync-from-ipam", nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
}

func TestSyncFromIPAM_NotFoundIs404(t *testing.T) {
	f := &fakeSyncQ{zoneErr: pgx.ErrNoRows}
	rec := authed(t, mountSync(f), "POST", "/dns/zones/"+uuid.New().String()+"/sync-from-ipam", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestSyncFromIPAM_BadUUIDIs400(t *testing.T) {
	rec := authed(t, mountSync(&fakeSyncQ{}),
		"POST", "/dns/zones/not-a-uuid/sync-from-ipam", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestSyncFromIPAM_RequiresUpdateCap(t *testing.T) {
	req := httptest.NewRequest("POST", "/dns/zones/"+uuid.New().String()+"/sync-from-ipam", nil)
	p := auth.Principal{Capabilities: []string{"dns:zones:read"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountSync(&fakeSyncQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestSyncFromIPAM_ReusesExistingReverseZone(t *testing.T) {
	// Reverse zone already in revZones — emit should reuse it
	// without calling CreateReverseZone.
	siteID := uuid.New()
	zoneID := uuid.New()
	fabricID := uuid.New()
	dnsName := "host1"
	existingRev := dbq.DnsZone{
		ID: uuid.New(), Name: "0.0.10.in-addr.arpa",
		FabricID: fabricID, SiteID: &siteID,
	}
	f := &fakeSyncQ{
		zone: dbq.DnsZone{ID: zoneID, FabricID: fabricID, SiteID: &siteID,
			Kind: "site", Name: "example.com"},
		revZones: []dbq.DnsZone{existingRev},
		ips: []dbq.ListIPAddressesForSiteWithDnsNameRow{
			{ID: uuid.New(), Address: "10.0.0.5", Source: "static", DnsName: &dnsName},
		},
	}
	rec := authed(t, mountSync(f), "POST", "/dns/zones/"+zoneID.String()+"/sync-from-ipam", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	// PTR (gotCreates[1]) should be in the existing reverse zone.
	if f.gotCreates[1].ZoneID != existingRev.ID {
		t.Errorf("PTR not placed in pre-existing reverse zone")
	}
}
