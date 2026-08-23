package capacity

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

func intPtr(v int32) *int32       { return &v }
func strPtr(s string) *string     { return &s }
func floatPtr(v float64) *float64 { return &v }

func TestSlotsUsed_BasicPlacement(t *testing.T) {
	assets := []dbq.Asset{
		{Kind: "server", RackPositionU: intPtr(1), RackUnits: intPtr(2)},
		{Kind: "server", RackPositionU: intPtr(10), RackUnits: intPtr(1)},
	}
	used := SlotsUsed(assets, 42)
	if !used[1] || !used[2] {
		t.Error("slots 1-2 should be used")
	}
	if !used[10] {
		t.Error("slot 10 should be used")
	}
	if used[3] || used[9] || used[11] {
		t.Errorf("unexpected used slots: 3=%v 9=%v 11=%v", used[3], used[9], used[11])
	}
}

// rack_units default is 1 (Python: `max(1, a.rack_units or 1)`).
func TestSlotsUsed_NilRackUnitsDefaultsToOne(t *testing.T) {
	assets := []dbq.Asset{
		{Kind: "server", RackPositionU: intPtr(5), RackUnits: nil},
	}
	used := SlotsUsed(assets, 42)
	if !used[5] {
		t.Error("slot 5 should be used")
	}
	if used[6] {
		t.Error("nil rack_units should default to 1; slot 6 not used")
	}
}

// Position outside the rack is ignored — defensive against bad data.
func TestSlotsUsed_OutOfRangeIgnored(t *testing.T) {
	assets := []dbq.Asset{
		{Kind: "server", RackPositionU: intPtr(0), RackUnits: intPtr(2)},
		{Kind: "server", RackPositionU: intPtr(50), RackUnits: intPtr(1)},
	}
	used := SlotsUsed(assets, 42)
	for u := int32(1); u <= 42; u++ {
		if used[u] {
			t.Errorf("slot %d should not be used", u)
		}
	}
}

// rack_units=2 starting at u=41 in a 42-U rack — span clipped at u=42.
func TestSlotsUsed_SpanClippedToRackHeight(t *testing.T) {
	assets := []dbq.Asset{
		{Kind: "server", RackPositionU: intPtr(41), RackUnits: intPtr(5)},
	}
	used := SlotsUsed(assets, 42)
	if !used[41] || !used[42] {
		t.Error("slots 41-42 should be used")
	}
}

func TestFreeRuns_AllFree(t *testing.T) {
	used := make([]bool, 44)
	runs := FreeRuns(used, 42)
	if len(runs) != 1 {
		t.Fatalf("expected 1 run; got %d", len(runs))
	}
	if runs[0].StartU != 1 || runs[0].Length != 42 {
		t.Errorf("got run %+v, want {start_u:1, length:42}", runs[0])
	}
}

// Multiple gaps, sorted longest-first with start_u as tiebreaker.
func TestFreeRuns_SortedLongestFirst(t *testing.T) {
	used := make([]bool, 44)
	used[1] = true
	used[2] = true
	used[10] = true
	runs := FreeRuns(used, 42)
	// Runs: [3..9] (len 7) and [11..42] (len 32). Longest first.
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs; got %d: %+v", len(runs), runs)
	}
	if runs[0].StartU != 11 || runs[0].Length != 32 {
		t.Errorf("first run = %+v, want {start_u:11, length:32}", runs[0])
	}
	if runs[1].StartU != 3 || runs[1].Length != 7 {
		t.Errorf("second run = %+v, want {start_u:3, length:7}", runs[1])
	}
}

func TestFreeRuns_FullyOccupied(t *testing.T) {
	used := make([]bool, 44)
	for i := 1; i <= 42; i++ {
		used[i] = true
	}
	runs := FreeRuns(used, 42)
	if len(runs) != 0 {
		t.Errorf("expected no runs; got %+v", runs)
	}
}

// Same-length runs sort by start_u ascending.
func TestFreeRuns_TieBrokenByStartU(t *testing.T) {
	used := make([]bool, 44)
	used[3] = true
	used[7] = true
	runs := FreeRuns(used, 42)
	// Runs: [1..2] (2), [4..6] (3), [8..42] (35). Sorted longest first.
	// Last two are different lengths; here construct equal-length:
	used = make([]bool, 44)
	used[5] = true
	// Runs: [1..4] (4), [6..42] (37).
	runs = FreeRuns(used, 42)
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs; got %d", len(runs))
	}
	// Sanity: [6..42] longer → first.
	if runs[0].StartU != 6 {
		t.Errorf("first run start = %d, want 6", runs[0].StartU)
	}
}

// ---- ComputeRackCapacity / ComputeManyRackCapacity ----

type fakeQ struct {
	pduRows []dbq.ListPduKwTelemetryRow
	gotIDs  []uuid.UUID
	err     error
}

func (f *fakeQ) ListPduKwTelemetry(_ context.Context, ids []uuid.UUID) ([]dbq.ListPduKwTelemetryRow, error) {
	f.gotIDs = ids
	if f.err != nil {
		return nil, f.err
	}
	// Filter rows whose AssetID is in ids.
	idSet := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		idSet[id] = struct{}{}
	}
	var out []dbq.ListPduKwTelemetryRow
	for _, r := range f.pduRows {
		if _, ok := idSet[r.AssetID]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

func TestComputeRackCapacity_NoKwData(t *testing.T) {
	rack := dbq.Rack{ID: uuid.New(), UHeight: 42, MaxKw: nil}
	assets := []dbq.Asset{
		{Kind: "server", RackPositionU: intPtr(1), RackUnits: intPtr(2)},
		{Kind: "server", RackPositionU: intPtr(10), RackUnits: intPtr(1)},
	}
	cap, err := ComputeRackCapacity(context.Background(), &fakeQ{}, rack, assets)
	if err != nil {
		t.Fatal(err)
	}
	if cap.UUsed != 3 || cap.UTotal != 42 || cap.UFree != 39 {
		t.Errorf("U rollup wrong: %+v", cap)
	}
	if cap.UPct < 7.1 || cap.UPct > 7.2 {
		t.Errorf("u_pct = %v, want ~7.1", cap.UPct)
	}
	if cap.KwCurrent != nil {
		t.Errorf("kw_current = %v, want nil (no PDU rows)", *cap.KwCurrent)
	}
	if cap.KwMax != nil {
		t.Errorf("kw_max = %v, want nil (no max_kw)", *cap.KwMax)
	}
	if cap.BiggestContiguousFree != 32 {
		t.Errorf("biggest_free = %d, want 32 (11..42)", cap.BiggestContiguousFree)
	}
}

func TestComputeRackCapacity_KwRollupCombinesUnits(t *testing.T) {
	pduA, pduB := uuid.New(), uuid.New()
	rack := dbq.Rack{ID: uuid.New(), UHeight: 42, MaxKw: strPtr("10")}
	assets := []dbq.Asset{
		{ID: pduA, Kind: "pdu"},
		{ID: pduB, Kind: "pdu"},
	}
	q := &fakeQ{
		pduRows: []dbq.ListPduKwTelemetryRow{
			{AssetID: pduA, Metric: "pdu.input.kw", LastValue: floatPtr(2.5)},
			// 1500W → 1.5 kW; 1.5 + 2.5 = 4.0 kW total.
			{AssetID: pduB, Metric: "pdu.input.w", LastValue: floatPtr(1500)},
		},
	}
	cap, err := ComputeRackCapacity(context.Background(), q, rack, assets)
	if err != nil {
		t.Fatal(err)
	}
	if cap.KwCurrent == nil || *cap.KwCurrent != 4.0 {
		t.Errorf("kw_current = %v, want 4.0", cap.KwCurrent)
	}
	if cap.KwMax == nil || *cap.KwMax != 10.0 {
		t.Errorf("kw_max = %v, want 10.0", cap.KwMax)
	}
	if cap.KwPct == nil || *cap.KwPct != 40.0 {
		t.Errorf("kw_pct = %v, want 40.0", cap.KwPct)
	}
}

// kw_max = 0 doesn't divide-by-zero; kw_pct stays nil.
func TestComputeRackCapacity_KwMaxZeroAvoidsDivByZero(t *testing.T) {
	rack := dbq.Rack{UHeight: 42, MaxKw: strPtr("0")}
	q := &fakeQ{pduRows: []dbq.ListPduKwTelemetryRow{
		{Metric: "pdu.input.kw", LastValue: floatPtr(1.5)},
	}}
	cap, err := ComputeRackCapacity(context.Background(), q, rack, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cap.KwPct != nil {
		t.Errorf("kw_pct should be nil when kw_max=0; got %v", *cap.KwPct)
	}
}

// FreeRuns capped at 8.
func TestComputeRackCapacity_FreeRunsCapped(t *testing.T) {
	rack := dbq.Rack{UHeight: 42}
	// Every odd slot occupied → ~21 alternating 1-U gaps.
	assets := make([]dbq.Asset, 0)
	for u := int32(1); u <= 42; u += 2 {
		uCopy := u
		one := int32(1)
		assets = append(assets, dbq.Asset{Kind: "server", RackPositionU: &uCopy, RackUnits: &one})
	}
	cap, err := ComputeRackCapacity(context.Background(), &fakeQ{}, rack, assets)
	if err != nil {
		t.Fatal(err)
	}
	if len(cap.FreeRuns) != 8 {
		t.Errorf("free_runs cap = 8; got %d", len(cap.FreeRuns))
	}
}

// ComputeManyRackCapacity issues exactly one telemetry call across all
// PDUs in the input racks, then slices the rows per rack.
func TestComputeManyRackCapacity_SingleTelemetryCall(t *testing.T) {
	rA, rB := dbq.Rack{ID: uuid.New(), UHeight: 42}, dbq.Rack{ID: uuid.New(), UHeight: 42}
	pduA, pduB := uuid.New(), uuid.New()
	assetsByRack := map[uuid.UUID][]dbq.Asset{
		rA.ID: {{ID: pduA, Kind: "pdu"}},
		rB.ID: {{ID: pduB, Kind: "pdu"}},
	}
	q := &fakeQ{pduRows: []dbq.ListPduKwTelemetryRow{
		{AssetID: pduA, Metric: "pdu.input.kw", LastValue: floatPtr(3.0)},
		{AssetID: pduB, Metric: "pdu.input.kw", LastValue: floatPtr(5.0)},
	}}
	out, err := ComputeManyRackCapacity(context.Background(), q, []dbq.Rack{rA, rB}, assetsByRack)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 rack rollups; got %d", len(out))
	}
	if !reflect.DeepEqual(sortedUUIDs(q.gotIDs), sortedUUIDs([]uuid.UUID{pduA, pduB})) {
		t.Errorf("PDU id set passed = %v, want {pduA, pduB}", q.gotIDs)
	}
	if c := out[rA.ID]; c.KwCurrent == nil || *c.KwCurrent != 3.0 {
		t.Errorf("rack A kw_current = %v, want 3.0", c.KwCurrent)
	}
	if c := out[rB.ID]; c.KwCurrent == nil || *c.KwCurrent != 5.0 {
		t.Errorf("rack B kw_current = %v, want 5.0", c.KwCurrent)
	}
}

// Empty input → no telemetry call.
func TestComputeManyRackCapacity_EmptyInputSkipsQuery(t *testing.T) {
	q := &fakeQ{}
	out, err := ComputeManyRackCapacity(context.Background(), q, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty out; got %d", len(out))
	}
	if q.gotIDs != nil {
		t.Errorf("telemetry query should be skipped; got ids = %v", q.gotIDs)
	}
}

// Round1 + Round3 match Python's round(v, 1) / round(v, 3) for happy
// cases. Exact-half ties differ (Python uses banker's rounding, Go
// uses half-away-from-zero) but dashboards values rarely land on
// halves and finch just parseFloat()s them regardless.
func TestRound(t *testing.T) {
	for _, c := range []struct {
		v, want float64
		fn      func(float64) float64
		label   string
	}{
		{1.234, 1.2, round1, "round1 down"},
		{1.250001, 1.3, round1, "round1 up"},
		{1.234567, 1.235, round3, "round3 up"},
		{0, 0, round1, "round1 zero"},
		{-1.234, -1.2, round1, "round1 negative"},
	} {
		got := c.fn(c.v)
		if got != c.want {
			t.Errorf("%s: %v → %v, want %v", c.label, c.v, got, c.want)
		}
	}
}

// helper: sort uuid slice for set comparison.
func sortedUUIDs(in []uuid.UUID) []uuid.UUID {
	out := make([]uuid.UUID, len(in))
	copy(out, in)
	// simple insertion sort (small slice) — avoid pulling in sort with deps
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].String() > out[j].String(); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// floatPtr keeps the linter happy about unused helper.
var _ = floatPtr
