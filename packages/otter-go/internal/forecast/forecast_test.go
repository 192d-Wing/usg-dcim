package forecast

import (
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

func uPtr(v int32) *int32     { return &v }
func fPtr(v float64) *float64 { return &v }
func strPtr(s string) *string { return &s }

// Most ComputeRackForecast paths route through linearSlope; cover them
// here so the handler-side tests can focus on wire shape.

func TestComputeRackForecast_RackAlreadyFull(t *testing.T) {
	rack := dbq.Rack{ID: uuid.New(), UHeight: 4}
	// 4 1-U servers placed at slots 1..4 → uFree = 0
	now := time.Now().UTC()
	earlier := now.Add(-30 * 24 * time.Hour)
	var assets []dbq.Asset
	for i := int32(1); i <= 4; i++ {
		assets = append(assets, dbq.Asset{
			ID: uuid.New(), Mount: "rack",
			RackPositionU: uPtr(i), RackUnits: uPtr(1),
			CreatedAt: earlier,
		})
	}
	f := ComputeRackForecast(rack, assets, now)
	if f.UUsed != 4 || f.UFree != 0 {
		t.Errorf("u_used=%d u_free=%d, want 4/0", f.UUsed, f.UFree)
	}
	if f.RunwayBand != "critical" {
		t.Errorf("runway_band = %q, want critical", f.RunwayBand)
	}
	if f.SlopeUPerDay != nil || f.DaysUntilFull != nil {
		t.Errorf("slope/days should be nil for full rack")
	}
}

func TestComputeRackForecast_FewerThanTwoSamplesUnknown(t *testing.T) {
	rack := dbq.Rack{ID: uuid.New(), UHeight: 42}
	now := time.Now().UTC()
	assets := []dbq.Asset{
		{ID: uuid.New(), Mount: "rack", RackPositionU: uPtr(1), RackUnits: uPtr(1), CreatedAt: now.Add(-time.Hour)},
	}
	f := ComputeRackForecast(rack, assets, now)
	if f.UUsed != 1 {
		t.Errorf("u_used = %d, want 1", f.UUsed)
	}
	if f.RunwayBand != "unknown" {
		t.Errorf("runway_band = %q, want unknown (n<2)", f.RunwayBand)
	}
	if f.SlopeUPerDay != nil {
		t.Errorf("slope should be nil for n<2")
	}
}

// Linear growth → slope ~= 1 U/day, days_until_full = u_free * days_per_u.
func TestComputeRackForecast_LinearGrowth(t *testing.T) {
	rack := dbq.Rack{ID: uuid.New(), UHeight: 10}
	now := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	// One 1-U asset created per day on 2026-01-01..2026-01-04 → 4 used.
	var assets []dbq.Asset
	for i := int32(0); i < 4; i++ {
		assets = append(assets, dbq.Asset{
			ID: uuid.New(), Mount: "rack",
			RackPositionU: uPtr(i + 1), RackUnits: uPtr(1),
			CreatedAt: time.Date(2026, 1, 1+int(i), 0, 0, 0, 0, time.UTC),
		})
	}
	f := ComputeRackForecast(rack, assets, now)
	if f.UUsed != 4 || f.UFree != 6 {
		t.Errorf("uUsed=%d uFree=%d, want 4/6", f.UUsed, f.UFree)
	}
	if f.SlopeUPerDay == nil {
		t.Fatal("slope should be populated")
	}
	if *f.SlopeUPerDay < 0.9 || *f.SlopeUPerDay > 1.1 {
		t.Errorf("slope = %v, want ~1.0 (1U/day)", *f.SlopeUPerDay)
	}
	if f.DaysUntilFull == nil || *f.DaysUntilFull < 5.5 || *f.DaysUntilFull > 6.5 {
		t.Errorf("days_until_full = %v, want ~6", f.DaysUntilFull)
	}
	if f.RunwayBand != "critical" {
		t.Errorf("runway_band = %q, want critical (< 30 days)", f.RunwayBand)
	}
}

// Excluded asset kinds: anything with Mount != "rack" or no
// rack_position_u is ignored.
func TestComputeRackForecast_FiltersUnplacedAssets(t *testing.T) {
	rack := dbq.Rack{ID: uuid.New(), UHeight: 10}
	now := time.Now().UTC()
	long := now.Add(-30 * 24 * time.Hour)
	assets := []dbq.Asset{
		{ID: uuid.New(), Mount: "rack", RackPositionU: nil, RackUnits: uPtr(1), CreatedAt: long},
		{ID: uuid.New(), Mount: "rack", RackPositionU: uPtr(0), RackUnits: uPtr(1), CreatedAt: long},
		{ID: uuid.New(), Mount: "rack-side", RackPositionU: uPtr(1), RackUnits: uPtr(1), CreatedAt: long},
	}
	f := ComputeRackForecast(rack, assets, now)
	if f.UUsed != 0 {
		t.Errorf("unplaced/non-rack assets should not contribute; uUsed=%d", f.UUsed)
	}
}

// ---- what-if ----

func TestComputeWhatIf_FreeRoomNoGrowth(t *testing.T) {
	rack := dbq.Rack{ID: uuid.New(), UHeight: 10}
	// Single asset → no slope, but what-if can still compute u_free.
	now := time.Now().UTC()
	assets := []dbq.Asset{
		{ID: uuid.New(), Mount: "rack", RackPositionU: uPtr(1), RackUnits: uPtr(1), CreatedAt: now.Add(-time.Hour)},
	}
	w := ComputeWhatIf(rack, assets, 4, now)
	if w.WhatIfUUsed != 5 || w.WhatIfUFree != 5 {
		t.Errorf("what-if u: used=%d free=%d, want 5/5", w.WhatIfUUsed, w.WhatIfUFree)
	}
	// No slope → no projected days
	if w.WhatIfDaysUntilFull != nil {
		t.Errorf("days_until_full should be nil with no slope; got %v", *w.WhatIfDaysUntilFull)
	}
}

func TestComputeWhatIf_AddPushesToFull(t *testing.T) {
	rack := dbq.Rack{ID: uuid.New(), UHeight: 4}
	now := time.Now().UTC()
	earlier := now.Add(-30 * 24 * time.Hour)
	assets := []dbq.Asset{
		{ID: uuid.New(), Mount: "rack", RackPositionU: uPtr(1), RackUnits: uPtr(1), CreatedAt: earlier},
		{ID: uuid.New(), Mount: "rack", RackPositionU: uPtr(2), RackUnits: uPtr(1), CreatedAt: earlier.Add(time.Hour)},
	}
	w := ComputeWhatIf(rack, assets, 2, now)
	if w.WhatIfUUsed != 4 || w.WhatIfUFree != 0 {
		t.Errorf("what-if pushes to full: used=%d free=%d, want 4/0", w.WhatIfUUsed, w.WhatIfUFree)
	}
	if w.WhatIfDaysUntilFull == nil || *w.WhatIfDaysUntilFull != 0 {
		t.Errorf("days_until_full = %v, want 0.0 (already full)", w.WhatIfDaysUntilFull)
	}
	if w.WhatIfRunwayBand != "critical" {
		t.Errorf("runway band = %q, want critical", w.WhatIfRunwayBand)
	}
}

// ---- kW projection ----

func TestProjectKw_NoSamples(t *testing.T) {
	now := time.Now().UTC()
	f := ProjectKw(nil, fPtr(10.0), 90, now)
	if f.Samples != 0 || f.SlopeKwPerDay != nil {
		t.Errorf("empty samples should yield 0/nil; got %+v", f)
	}
	if f.RunwayBand != "unknown" {
		t.Errorf("runway band = %q, want unknown", f.RunwayBand)
	}
}

func TestProjectKw_LinearGrowthHitsMax(t *testing.T) {
	now := time.Date(2026, 5, 30, 0, 0, 0, 0, time.UTC)
	// 5 daily samples 6.0 → 7.0 → 8.0 → 9.0 → 10.0 (slope 1 kW/day).
	samples := []TimedValue{
		{T: now.Add(-4 * 24 * time.Hour), Value: 6.0},
		{T: now.Add(-3 * 24 * time.Hour), Value: 7.0},
		{T: now.Add(-2 * 24 * time.Hour), Value: 8.0},
		{T: now.Add(-1 * 24 * time.Hour), Value: 9.0},
		{T: now, Value: 10.0},
	}
	f := ProjectKw(samples, fPtr(15.0), 90, now)
	if f.SlopeKwPerDay == nil || *f.SlopeKwPerDay < 0.9 || *f.SlopeKwPerDay > 1.1 {
		t.Errorf("slope = %v, want ~1.0", f.SlopeKwPerDay)
	}
	if f.CurrentKw == nil || *f.CurrentKw != 10.0 {
		t.Errorf("current_kw = %v, want 10.0", f.CurrentKw)
	}
	// headroom = 5, slope = 1 → days_until_max ~= 5
	if f.DaysUntilMax == nil || *f.DaysUntilMax < 4.5 || *f.DaysUntilMax > 5.5 {
		t.Errorf("days_until_max = %v, want ~5", f.DaysUntilMax)
	}
	if f.RunwayBand != "critical" {
		t.Errorf("runway band = %q, want critical", f.RunwayBand)
	}
}

func TestProjectKw_AlreadyOverMax(t *testing.T) {
	now := time.Now().UTC()
	samples := []TimedValue{
		{T: now.Add(-2 * 24 * time.Hour), Value: 5.0},
		{T: now, Value: 15.0},
	}
	f := ProjectKw(samples, fPtr(10.0), 90, now)
	if f.DaysUntilMax == nil || *f.DaysUntilMax != 0 {
		t.Errorf("days_until_max = %v, want 0", f.DaysUntilMax)
	}
	if f.RunwayBand != "critical" {
		t.Errorf("runway band = %q, want critical", f.RunwayBand)
	}
}

// SamplesFromRows: W → kW conversion, daily sum across metrics.
func TestSamplesFromRows_WConversionAndDailySum(t *testing.T) {
	day1 := time.Date(2026, 5, 28, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)
	rows := []dbq.ListKwHistorySamplesRow{
		{Day: day1, Metric: "pdu.input.kw", AvgV: 2.5},
		{Day: day1, Metric: "pdu.input.w", AvgV: 1500.0}, // → 1.5 kW
		{Day: day2, Metric: "pdu.input.kw", AvgV: 4.0},
	}
	samples := SamplesFromRows(rows)
	if len(samples) != 2 {
		t.Fatalf("expected 2 samples; got %d", len(samples))
	}
	// day1 sum = 2.5 + 1.5 = 4.0
	if samples[0].T != day1 || samples[0].Value != 4.0 {
		t.Errorf("day1: %+v, want {%v, 4.0}", samples[0], day1)
	}
	if samples[1].T != day2 || samples[1].Value != 4.0 {
		t.Errorf("day2: %+v, want {%v, 4.0}", samples[1], day2)
	}
}

// ---- site forecast ----

func TestComputeSiteForecast_NoRacks(t *testing.T) {
	siteID := uuid.New()
	f := ComputeSiteForecast(siteID, nil, nil, time.Now().UTC())
	if f.RackCount != 0 || f.UTotal != 0 {
		t.Errorf("empty site: %+v", f)
	}
	if f.MinRunwayDays != nil {
		t.Errorf("min_runway should be nil")
	}
}

func TestComputeSiteForecast_TrackBands(t *testing.T) {
	siteID := uuid.New()
	r1, r2 := uuid.New(), uuid.New()
	racks := []dbq.Rack{
		{ID: r1, UHeight: 10},
		{ID: r2, UHeight: 10},
	}
	// r1: full → critical
	// r2: 1 asset → unknown band (n<2)
	now := time.Now().UTC()
	earlier := now.Add(-30 * 24 * time.Hour)
	assetsByRack := map[uuid.UUID][]dbq.Asset{
		r1: {
			{ID: uuid.New(), Mount: "rack", RackPositionU: uPtr(1), RackUnits: uPtr(10), CreatedAt: earlier},
		},
		r2: {
			{ID: uuid.New(), Mount: "rack", RackPositionU: uPtr(1), RackUnits: uPtr(1), CreatedAt: earlier},
		},
	}
	f := ComputeSiteForecast(siteID, racks, assetsByRack, now)
	if f.RackCount != 2 {
		t.Errorf("rack_count = %d, want 2", f.RackCount)
	}
	if f.RacksCritical != 1 {
		t.Errorf("critical = %d, want 1 (r1 full)", f.RacksCritical)
	}
	// r1 contributes 10 to UUsed; r2 contributes 1.
	if f.UUsed != 11 || f.UTotal != 20 {
		t.Errorf("u: used=%d total=%d, want 11/20", f.UUsed, f.UTotal)
	}
}

func TestPowerMetrics_StableOrder(t *testing.T) {
	a := PowerMetrics()
	b := PowerMetrics()
	if !reflect.DeepEqual(a, b) {
		t.Errorf("PowerMetrics order not stable: %v vs %v", a, b)
	}
	if len(a) != 5 {
		t.Errorf("PowerMetrics len = %d, want 5", len(a))
	}
}

// silence unused-helper warnings
var _ = strPtr
