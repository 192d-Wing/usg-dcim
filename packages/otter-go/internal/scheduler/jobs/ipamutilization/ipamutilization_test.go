package ipamutilization

import (
	"context"
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

type fakeQ struct {
	subnets   []dbq.ListSubnetsForUtilizationRow
	supernets []dbq.ListSupernetsForUtilizationRow
	counts    []dbq.ListActiveReservedAddressCountsBySubnetRow
	subnetsErr,
	supernetsErr,
	countsErr error
}

func (f *fakeQ) ListSubnetsForUtilization(_ context.Context) ([]dbq.ListSubnetsForUtilizationRow, error) {
	return f.subnets, f.subnetsErr
}
func (f *fakeQ) ListSupernetsForUtilization(_ context.Context) ([]dbq.ListSupernetsForUtilizationRow, error) {
	return f.supernets, f.supernetsErr
}
func (f *fakeQ) ListActiveReservedAddressCountsBySubnet(_ context.Context) ([]dbq.ListActiveReservedAddressCountsBySubnetRow, error) {
	return f.counts, f.countsErr
}

// gaugeValue reads back a single labeled value from a GaugeVec via
// the Prometheus collect path. Returns NaN if the label set hasn't
// been emitted, so tests can fail loud on a missing value.
func gaugeValue(t *testing.T, gv *prometheus.GaugeVec, labels ...string) float64 {
	t.Helper()
	g, err := gv.GetMetricWithLabelValues(labels...)
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan prometheus.Metric, 1)
	g.Collect(ch)
	close(ch)
	for m := range ch {
		var pb dto.Metric
		if err := m.Write(&pb); err != nil {
			t.Fatal(err)
		}
		return pb.GetGauge().GetValue()
	}
	return math.NaN()
}

func TestRun_SubnetUtilization_Ipv4_HostsMinusTwo(t *testing.T) {
	// /24: 256 addresses, capacity = 254, used = 100 → free = 60.6%
	subnetID, fabID := uuid.New(), uuid.New()
	q := &fakeQ{
		subnets: []dbq.ListSubnetsForUtilizationRow{
			{ID: subnetID, FabricID: fabID, Prefix: "10.0.0.0/24"},
		},
		counts: []dbq.ListActiveReservedAddressCountsBySubnetRow{
			{SubnetID: subnetID, UsedCount: 100},
		},
	}
	reg := prometheus.NewRegistry()
	g := NewGauges(reg)
	res, err := (&Job{Q: q, Gauges: g}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res["subnets_emitted"].(int) != 1 {
		t.Fatalf("expected 1 subnet emitted, got %v", res["subnets_emitted"])
	}
	got := gaugeValue(t, g.SubnetFreePercent, fabID.String(), subnetID.String())
	want := 100.0 * (254 - 100) / 254
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("subnet free%% drift: got %f want %f", got, want)
	}
}

func TestRun_SubnetUtilization_PointToPointClampsTo100(t *testing.T) {
	// /32 has capacity 0 in Python's helper — there's nothing to use,
	// so free_percent = 100 even with used > 0.
	subnetID, fabID := uuid.New(), uuid.New()
	q := &fakeQ{
		subnets: []dbq.ListSubnetsForUtilizationRow{
			{ID: subnetID, FabricID: fabID, Prefix: "10.0.0.1/32"},
		},
		counts: []dbq.ListActiveReservedAddressCountsBySubnetRow{
			{SubnetID: subnetID, UsedCount: 1},
		},
	}
	reg := prometheus.NewRegistry()
	g := NewGauges(reg)
	_, _ = (&Job{Q: q, Gauges: g}).Run(context.Background())
	got := gaugeValue(t, g.SubnetFreePercent, fabID.String(), subnetID.String())
	if got != 100.0 {
		t.Errorf("/32 should clamp to 100%% free; got %f", got)
	}
}

func TestRun_SubnetUtilization_UsedClampedToCapacity(t *testing.T) {
	// /28 has capacity = 16-2 = 14. used = 9999 must clamp to 14 →
	// free = 0%, mirrors Python's `min(used, cap)`.
	subnetID, fabID := uuid.New(), uuid.New()
	q := &fakeQ{
		subnets: []dbq.ListSubnetsForUtilizationRow{
			{ID: subnetID, FabricID: fabID, Prefix: "10.0.0.0/28"},
		},
		counts: []dbq.ListActiveReservedAddressCountsBySubnetRow{
			{SubnetID: subnetID, UsedCount: 9999},
		},
	}
	reg := prometheus.NewRegistry()
	g := NewGauges(reg)
	_, _ = (&Job{Q: q, Gauges: g}).Run(context.Background())
	got := gaugeValue(t, g.SubnetFreePercent, fabID.String(), subnetID.String())
	if got != 0.0 {
		t.Errorf("expected 0%% free, got %f", got)
	}
}

func TestRun_SupernetUtilization_FoldsChildSubnetCapacities(t *testing.T) {
	// Supernet 10.0.0.0/16 has num_addresses = 65536 (no -2 deduct).
	// Two child subnets /24 + /24 → carved = 2 * 254 = 508.
	// Free = (65536 - 508) / 65536.
	supernetID, fabID := uuid.New(), uuid.New()
	s1ID, s2ID := uuid.New(), uuid.New()
	q := &fakeQ{
		subnets: []dbq.ListSubnetsForUtilizationRow{
			{ID: s1ID, FabricID: fabID, SupernetID: supernetID, Prefix: "10.0.0.0/24"},
			{ID: s2ID, FabricID: fabID, SupernetID: supernetID, Prefix: "10.0.1.0/24"},
		},
		supernets: []dbq.ListSupernetsForUtilizationRow{
			{ID: supernetID, FabricID: fabID, Prefix: "10.0.0.0/16"},
		},
	}
	reg := prometheus.NewRegistry()
	g := NewGauges(reg)
	res, err := (&Job{Q: q, Gauges: g}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res["supernets_emitted"].(int) != 1 {
		t.Fatalf("expected 1 supernet emitted, got %v", res["supernets_emitted"])
	}
	got := gaugeValue(t, g.SupernetFreePercent, fabID.String(), supernetID.String())
	want := 100.0 * (65536.0 - 508) / 65536.0
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("supernet free%% drift: got %f want %f", got, want)
	}
}

func TestRun_SubnetWithoutCountRow_TreatsUsedAsZero(t *testing.T) {
	// Subnets with no active/reserved addresses are absent from
	// ListActiveReservedAddressCountsBySubnet. The Go fold must read
	// used=0 for those — equivalent to Python's
	// `used_by_subnet.get(s.id, 0)`.
	subnetID, fabID := uuid.New(), uuid.New()
	q := &fakeQ{
		subnets: []dbq.ListSubnetsForUtilizationRow{
			{ID: subnetID, FabricID: fabID, Prefix: "10.0.0.0/24"},
		},
		counts: nil, // no rows
	}
	reg := prometheus.NewRegistry()
	g := NewGauges(reg)
	_, _ = (&Job{Q: q, Gauges: g}).Run(context.Background())
	got := gaugeValue(t, g.SubnetFreePercent, fabID.String(), subnetID.String())
	if got != 100.0 {
		t.Errorf("subnet with no count rows should read as 100%% free; got %f", got)
	}
}

func TestRun_OrphanedSubnetSupernetIDNil_NotFoldedIntoCarvedMap(t *testing.T) {
	// A subnet with NULL supernet_id (orphaned during a re-carve)
	// must not contribute to any supernet's carved_capacity. The
	// supernet result still emits as 100% free. NULL supernet_id now
	// arrives as uuid.Nil (SupernetID is a non-pointer uuid.UUID).
	supernetID, fabID := uuid.New(), uuid.New()
	q := &fakeQ{
		subnets: []dbq.ListSubnetsForUtilizationRow{
			{ID: uuid.New(), FabricID: fabID, SupernetID: uuid.Nil, Prefix: "10.0.0.0/24"},
		},
		supernets: []dbq.ListSupernetsForUtilizationRow{
			{ID: supernetID, FabricID: fabID, Prefix: "10.0.0.0/16"},
		},
	}
	reg := prometheus.NewRegistry()
	g := NewGauges(reg)
	_, _ = (&Job{Q: q, Gauges: g}).Run(context.Background())
	got := gaugeValue(t, g.SupernetFreePercent, fabID.String(), supernetID.String())
	if got != 100.0 {
		t.Errorf("supernet with no carved children should read 100%% free; got %f", got)
	}
}

func TestRun_BadPrefix_Skipped(t *testing.T) {
	// Python's helper catches TypeError/ValueError on ip_network()
	// and treats the row as capacity=0 → free=100. Match the
	// behavior so a malformed row doesn't break the sweep.
	subnetID, fabID := uuid.New(), uuid.New()
	q := &fakeQ{
		subnets: []dbq.ListSubnetsForUtilizationRow{
			{ID: subnetID, FabricID: fabID, Prefix: "this-is-not-a-cidr"},
		},
	}
	reg := prometheus.NewRegistry()
	g := NewGauges(reg)
	res, err := (&Job{Q: q, Gauges: g}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res["subnets_emitted"].(int) != 1 {
		t.Errorf("malformed prefix must still emit a gauge; got %v", res["subnets_emitted"])
	}
	got := gaugeValue(t, g.SubnetFreePercent, fabID.String(), subnetID.String())
	if got != 100.0 {
		t.Errorf("malformed prefix should read 100%% free; got %f", got)
	}
}

func TestRun_NilQuerier_Errors(t *testing.T) {
	g := NewGauges(prometheus.NewRegistry())
	if _, err := (&Job{Gauges: g}).Run(context.Background()); err == nil {
		t.Errorf("nil Querier should error")
	}
}

func TestRun_NilGauges_Errors(t *testing.T) {
	if _, err := (&Job{Q: &fakeQ{}}).Run(context.Background()); err == nil {
		t.Errorf("nil Gauges should error")
	}
}
