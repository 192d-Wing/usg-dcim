package dnssync

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// fakeQ is the slimmest implementation that drives Run end-to-end:
// ListAllSiteDnsZones returns the seeded slice; ListIPAddresses…
// returns the per-site seeded IPs; CountIPAMRecordsInZones returns
// dropPerZone (per call, so total removed = dropPerZone × zones).
// failCount, when true, makes CountIPAMRecordsInZones return an
// error on every call — sufficient to exercise the per-zone error
// path because the helper aborts on the first failed Count.
type fakeQ struct {
	zones        []dbq.DnsZone
	ipsBySite    map[uuid.UUID][]dbq.ListIPAddressesForSiteWithDnsNameRow
	dropPerZone  int64
	listZonesErr error
	failCount    bool
}

func (f *fakeQ) ListAllSiteDnsZones(_ context.Context) ([]dbq.DnsZone, error) {
	if f.listZonesErr != nil {
		return nil, f.listZonesErr
	}
	return f.zones, nil
}
func (f *fakeQ) ListReverseZonesForSite(_ context.Context, _ dbq.ListReverseZonesForSiteParams) ([]dbq.DnsZone, error) {
	return nil, nil
}
func (f *fakeQ) GetReverseZoneByName(_ context.Context, _ dbq.GetReverseZoneByNameParams) (dbq.DnsZone, error) {
	return dbq.DnsZone{}, pgx.ErrNoRows
}
func (f *fakeQ) CreateReverseZone(_ context.Context, arg dbq.CreateReverseZoneParams) (dbq.DnsZone, error) {
	siteID := arg.SiteID
	return dbq.DnsZone{ID: uuid.New(), Name: arg.Name, Kind: "reverse", FabricID: arg.FabricID, SiteID: &siteID}, nil
}
func (f *fakeQ) ListIPAddressesForSiteWithDnsName(_ context.Context, siteID uuid.UUID) ([]dbq.ListIPAddressesForSiteWithDnsNameRow, error) {
	return f.ipsBySite[siteID], nil
}
func (f *fakeQ) DeleteIPAMRecordsInZones(_ context.Context, _ []uuid.UUID) error { return nil }
func (f *fakeQ) CountIPAMRecordsInZones(_ context.Context, _ []uuid.UUID) (int64, error) {
	if f.failCount {
		return 0, errors.New("count failed")
	}
	return f.dropPerZone, nil
}
func (f *fakeQ) CreateProjectedDnsRecord(_ context.Context, _ dbq.CreateProjectedDnsRecordParams) (uuid.UUID, error) {
	return uuid.New(), nil
}
func (f *fakeQ) TouchDnsZone(_ context.Context, _ uuid.UUID) (int64, error) { return 1, nil }

func siteZone(t *testing.T) dbq.DnsZone {
	t.Helper()
	siteID := uuid.New()
	return dbq.DnsZone{
		ID: uuid.New(), Name: "site.example.com", Kind: "site",
		FabricID: uuid.New(), SiteID: &siteID,
	}
}

func TestRun_EmptyZoneList_ReportsZero(t *testing.T) {
	q := &fakeQ{}
	j := &Job{Q: q}
	out, err := j.Run(context.Background())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if v, _ := out["zones"].(int); v != 0 {
		t.Errorf("zones: got %v, want 0", out["zones"])
	}
	if v, _ := out["added"].(int); v != 0 {
		t.Errorf("added: got %v, want 0", out["added"])
	}
	if v, _ := out["removed"].(int); v != 0 {
		t.Errorf("removed: got %v, want 0", out["removed"])
	}
}

func TestRun_NilQuerier_Rejected(t *testing.T) {
	j := &Job{}
	if _, err := j.Run(context.Background()); err == nil {
		t.Error("expected error for nil Q")
	}
}

func TestRun_ListZonesError_Wrapped(t *testing.T) {
	q := &fakeQ{listZonesErr: errors.New("db gone")}
	j := &Job{Q: q}
	_, err := j.Run(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "list site zones") {
		t.Errorf("expected wrapped 'list site zones' error, got %q", err.Error())
	}
}

func TestRun_AggregatesAcrossZones(t *testing.T) {
	z1 := siteZone(t)
	z2 := siteZone(t)
	name1 := "host1.site.example.com"
	name2 := "host2.site.example.com"
	q := &fakeQ{
		zones: []dbq.DnsZone{z1, z2},
		ipsBySite: map[uuid.UUID][]dbq.ListIPAddressesForSiteWithDnsNameRow{
			*z1.SiteID: {{ID: uuid.New(), Address: "10.0.0.1", DnsName: &name1, Source: "ipam"}},
			*z2.SiteID: {{ID: uuid.New(), Address: "10.0.0.2", DnsName: &name2, Source: "ipam"}},
		},
		dropPerZone: 3,
	}
	j := &Job{Q: q}
	out, err := j.Run(context.Background())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if v := out["zones"].(int); v != 2 {
		t.Errorf("zones: got %d, want 2", v)
	}
	// Each zone: 2 records added (A + PTR), 3 dropped.
	if v := out["added"].(int); v != 4 {
		t.Errorf("added: got %d, want 4 (2 per zone × 2)", v)
	}
	if v := out["removed"].(int); v != 6 {
		t.Errorf("removed: got %d, want 6 (3 per zone × 2)", v)
	}
}

func TestRun_PerZoneError_AbortsWithZoneContext(t *testing.T) {
	z1 := siteZone(t)
	q := &fakeQ{
		zones:     []dbq.DnsZone{z1},
		failCount: true, // forces CountIPAMRecordsInZones to fail
	}
	j := &Job{Q: q}
	_, err := j.Run(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "zone "+z1.ID.String()) {
		t.Errorf("error should mention failing zone id; got %q", err.Error())
	}
}

func TestName_Matches(t *testing.T) {
	j := &Job{}
	if j.Name() != Name {
		t.Errorf("Name(): got %q, want %q", j.Name(), Name)
	}
	if Name != "dns_sync_from_ipam" {
		t.Errorf("package-level Name constant changed unexpectedly: %q", Name)
	}
}
