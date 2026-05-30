// Package forecast holds the capacity-forecasting helpers used by the
// /dashboards/forecast/* endpoints. Mirrors packages/otter/src/dcim/
// services/forecast.py byte-for-byte. Pure compute functions stay
// DB-free so tests don't need an integration harness; the kW path
// adds an internal/dashboards-side fetch that calls the
// telemetry_samples hypertable through dbq.
package forecast

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// noGrowthEps mirrors Python's _NO_GROWTH_EPS — the regression
// declines to project a fill date when the slope is below this.
const noGrowthEps = 1e-6

// powerMetricKw / powerMetricW match the metric name buckets
// internal/capacity uses. Duplicating the constants here keeps this
// package importable without pulling capacity (avoids cycles).
var (
	powerMetricKw = map[string]struct{}{
		"pdu.input.kw":      {},
		"power.consumed.kW": {},
		"rack.input.kw":     {},
	}
	powerMetricW = map[string]struct{}{
		"power.consumed.W": {},
		"pdu.input.w":      {},
	}
)

// PowerMetrics returns the union of both metric sets in a stable
// order, used as the `metrics` parameter to ListKwHistorySamples.
func PowerMetrics() []string {
	out := make([]string, 0, len(powerMetricKw)+len(powerMetricW))
	for m := range powerMetricKw {
		out = append(out, m)
	}
	for m := range powerMetricW {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// HistoryPoint is one (timestamp, u_used) sample used by the
// regression. Python encodes ts as ISO 8601; Go uses RFC3339Nano
// (V8's new Date() parses both).
type HistoryPoint struct {
	Ts    string `json:"ts"`
	UUsed int32  `json:"u_used"`
}

// RackForecast is the per-rack U-fill projection shape. Mirrors
// compute_rack_forecast() return dict byte-for-byte; the optional
// what-if cells are appended via WhatIfForecast for /forecast/racks/{id}.
type RackForecast struct {
	RackID            string         `json:"rack_id"`
	UUsed             int32          `json:"u_used"`
	UTotal            int32          `json:"u_total"`
	UFree             int32          `json:"u_free"`
	History           []HistoryPoint `json:"history"`
	SlopeUPerDay      *float64       `json:"slope_u_per_day"`
	DaysUntilFull     *float64       `json:"days_until_full"`
	ProjectedFillDate *string        `json:"projected_fill_date"`
	RunwayBand        string         `json:"runway_band"`
}

// WhatIfForecast extends RackForecast with the "add N units" delta.
// Same shape Python returns from compute_what_if() — base fields
// flattened in via embedded struct.
type WhatIfForecast struct {
	RackForecast
	WhatIfAddUnits      int32    `json:"what_if_add_units"`
	WhatIfUUsed         int32    `json:"what_if_u_used"`
	WhatIfUFree         int32    `json:"what_if_u_free"`
	WhatIfDaysUntilFull *float64 `json:"what_if_days_until_full"`
	WhatIfRunwayBand    string   `json:"what_if_runway_band"`
}

// KwForecast mirrors Python's _kw_payload() return shape. Optional
// pointer fields encode as JSON null when nil.
type KwForecast struct {
	MaxKw            *float64 `json:"max_kw"`
	Days             int32    `json:"days"`
	Samples          int      `json:"samples"`
	SlopeKwPerDay    *float64 `json:"slope_kw_per_day"`
	CurrentKw        *float64 `json:"current_kw"`
	DaysUntilMax     *float64 `json:"days_until_max"`
	ProjectedMaxDate *string  `json:"projected_max_date"`
	RunwayBand       string   `json:"runway_band"`
}

// SiteForecast mirrors compute_site_forecast() return dict.
type SiteForecast struct {
	SiteID          string   `json:"site_id"`
	RackCount       int      `json:"rack_count"`
	UUsed           int32    `json:"u_used"`
	UTotal          int32    `json:"u_total"`
	UPct            float64  `json:"u_pct"`
	MinRunwayDays   *float64 `json:"min_runway_days"`
	MinRunwayRackID *string  `json:"min_runway_rack_id"`
	RacksCritical   int      `json:"racks_critical"`
	RacksWarning    int      `json:"racks_warning"`
	RacksHealthy    int      `json:"racks_healthy"`
}

// ComputeRackForecast is the per-rack U-fill projection. Pure compute;
// no DB calls. Mirrors compute_rack_forecast() byte-for-byte.
func ComputeRackForecast(rack dbq.Rack, assets []dbq.Asset, now time.Time) RackForecast {
	times, cumulative := buildTimeline(assets)
	uTotal := rack.UHeight
	uUsed := int32(0)
	if len(cumulative) > 0 {
		uUsed = cumulative[len(cumulative)-1]
	}
	uFree := uTotal - uUsed
	if uFree < 0 {
		uFree = 0
	}
	history := historyPoints(times, cumulative)

	base := RackForecast{
		RackID: rack.ID.String(), UUsed: uUsed, UTotal: uTotal, UFree: uFree,
		History: history, RunwayBand: "unknown",
	}
	if uFree == 0 {
		base.RunwayBand = "critical"
		return base
	}
	if len(times) < 2 {
		return base
	}

	daysX := make([]float64, len(times))
	for i, t := range times {
		daysX[i] = t.Sub(times[0]).Seconds() / 86400.0
	}
	cumF := make([]float64, len(cumulative))
	for i, v := range cumulative {
		cumF[i] = float64(v)
	}
	slope, ok := linearSlope(daysX, cumF)
	if !ok || slope < noGrowthEps {
		base.RunwayBand = "healthy"
		return base
	}

	days := float64(uFree) / slope
	fill := now.Add(time.Duration(days * 24 * float64(time.Hour)))
	fillStr := fill.UTC().Format(time.RFC3339Nano)
	roundedSlope := round4(slope)
	roundedDays := round1(days)
	base.SlopeUPerDay = &roundedSlope
	base.DaysUntilFull = &roundedDays
	base.ProjectedFillDate = &fillStr
	base.RunwayBand = runwayBand(days)
	return base
}

// ComputeWhatIf projects the runway impact of adding `addUnits` U.
// Reuses ComputeRackForecast for the base slope, then recomputes the
// days_until_full assuming the snapshot jumps by addUnits.
func ComputeWhatIf(rack dbq.Rack, assets []dbq.Asset, addUnits int32, now time.Time) WhatIfForecast {
	base := ComputeRackForecast(rack, assets, now)
	addClamped := addUnits
	if addClamped < 0 {
		addClamped = 0
	}
	newUUsed := base.UUsed + addClamped
	if newUUsed > rack.UHeight {
		newUUsed = rack.UHeight
	}
	newUFree := rack.UHeight - newUUsed
	if newUFree < 0 {
		newUFree = 0
	}
	out := WhatIfForecast{
		RackForecast:   base,
		WhatIfAddUnits: addUnits,
		WhatIfUUsed:    newUUsed,
		WhatIfUFree:    newUFree,
	}
	if base.SlopeUPerDay == nil || *base.SlopeUPerDay < noGrowthEps || newUFree == 0 {
		if newUFree == 0 {
			zero := 0.0
			out.WhatIfDaysUntilFull = &zero
			out.WhatIfRunwayBand = "critical"
		} else {
			out.WhatIfRunwayBand = base.RunwayBand
		}
		return out
	}
	days := float64(newUFree) / *base.SlopeUPerDay
	rounded := round1(days)
	out.WhatIfDaysUntilFull = &rounded
	out.WhatIfRunwayBand = runwayBand(days)
	return out
}

// ProjectKw is the pure projection step — given parsed (day, kW)
// samples, returns the kW forecast payload. Extracted so tests don't
// need TimescaleDB.
func ProjectKw(samples []TimedValue, maxKw *float64, days int32, now time.Time) KwForecast {
	var currentKw *float64
	if len(samples) > 0 {
		c := samples[len(samples)-1].Value
		currentKw = &c
	}
	out := KwForecast{
		MaxKw: maxKw, Days: days, Samples: len(samples),
		CurrentKw: currentKw, RunwayBand: "unknown",
	}
	if len(samples) < 2 {
		return out
	}
	slope, ok := slopeFromBuckets(samples)
	if ok {
		s := round6(slope)
		out.SlopeKwPerDay = &s
	}
	noGrowth := !ok || slope < noGrowthEps
	if noGrowth || maxKw == nil || currentKw == nil {
		if noGrowth {
			out.RunwayBand = "healthy"
		}
		return out
	}
	headroom := *maxKw - *currentKw
	if headroom <= 0 {
		zero := 0.0
		out.DaysUntilMax = &zero
		nowStr := now.UTC().Format(time.RFC3339Nano)
		out.ProjectedMaxDate = &nowStr
		out.RunwayBand = "critical"
		return out
	}
	daysUntil := headroom / slope
	rounded := round1(daysUntil)
	out.DaysUntilMax = &rounded
	proj := now.Add(time.Duration(daysUntil * 24 * float64(time.Hour))).UTC().Format(time.RFC3339Nano)
	out.ProjectedMaxDate = &proj
	out.RunwayBand = runwayBand(daysUntil)
	return out
}

// SamplesFromRows folds the (day, metric, avg_v) hypertable rows into
// one (day, total_kW) pair per day. W → kW conversion for metrics
// in powerMetricW. Sorted ascending by day.
func SamplesFromRows(rows []dbq.KwHistoryRow) []TimedValue {
	totals := map[time.Time]float64{}
	for _, r := range rows {
		if r.AvgV == nil {
			continue
		}
		v := *r.AvgV
		if _, isW := powerMetricW[r.Metric]; isW {
			v /= 1000.0
		}
		totals[r.Day] = totals[r.Day] + v
	}
	out := make([]TimedValue, 0, len(totals))
	for t, v := range totals {
		out = append(out, TimedValue{T: t, Value: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T.Before(out[j].T) })
	return out
}

// ComputeSiteForecast aggregates per-rack U + worst runway. Caller
// loads racks + assets-by-rack (see internal/dashboards/site_forecast).
func ComputeSiteForecast(siteID uuid.UUID, racks []dbq.Rack, assetsByRack map[uuid.UUID][]dbq.Asset, now time.Time) SiteForecast {
	out := SiteForecast{SiteID: siteID.String(), RackCount: len(racks)}
	if len(racks) == 0 {
		return out
	}
	var minRunway *float64
	var minRackID *uuid.UUID
	for _, r := range racks {
		f := ComputeRackForecast(r, assetsByRack[r.ID], now)
		out.UUsed += f.UUsed
		out.UTotal += f.UTotal
		if f.DaysUntilFull != nil {
			if minRunway == nil || *f.DaysUntilFull < *minRunway {
				d := *f.DaysUntilFull
				minRunway = &d
				rID := r.ID
				minRackID = &rID
			}
		}
		switch f.RunwayBand {
		case "critical":
			out.RacksCritical++
		case "warning":
			out.RacksWarning++
		case "healthy":
			out.RacksHealthy++
		}
	}
	if out.UTotal > 0 {
		out.UPct = round1(100.0 * float64(out.UUsed) / float64(out.UTotal))
	}
	out.MinRunwayDays = minRunway
	if minRackID != nil {
		s := minRackID.String()
		out.MinRunwayRackID = &s
	}
	return out
}

// TimedValue is one (timestamp, value) sample used by SamplesFromRows
// + ProjectKw. Exported so the kW-fetch path in internal/dashboards
// can hand-craft samples in tests.
type TimedValue struct {
	T     time.Time
	Value float64
}

// FetchKwHistory wraps the ListKwHistorySamples call for the kW
// forecast. Extracted so a fake querier in the test can override.
func FetchKwHistory(
	ctx context.Context, q KwHistoryQuerier,
	pduIDs []uuid.UUID, start, end time.Time,
) ([]dbq.KwHistoryRow, error) {
	return q.ListKwHistorySamples(ctx, dbq.ListKwHistorySamplesParams{
		AssetIDs: pduIDs, Metrics: PowerMetrics(), Start: start, End: end,
	})
}

type KwHistoryQuerier interface {
	ListKwHistorySamples(ctx context.Context, arg dbq.ListKwHistorySamplesParams) ([]dbq.KwHistoryRow, error)
}

// ---- pure helpers ----

func buildTimeline(assets []dbq.Asset) ([]time.Time, []int32) {
	placed := make([]dbq.Asset, 0, len(assets))
	for _, a := range assets {
		if a.RackPositionU == nil || *a.RackPositionU == 0 {
			continue
		}
		if a.Mount != "rack" {
			continue
		}
		placed = append(placed, a)
	}
	sort.Slice(placed, func(i, j int) bool { return placed[i].CreatedAt.Before(placed[j].CreatedAt) })
	times := make([]time.Time, 0, len(placed))
	cum := make([]int32, 0, len(placed))
	total := int32(0)
	for _, a := range placed {
		u := int32(1)
		if a.RackUnits != nil && *a.RackUnits > 1 {
			u = *a.RackUnits
		}
		total += u
		times = append(times, a.CreatedAt)
		cum = append(cum, total)
	}
	return times, cum
}

func historyPoints(times []time.Time, cum []int32) []HistoryPoint {
	out := make([]HistoryPoint, 0, len(times))
	for i, t := range times {
		out = append(out, HistoryPoint{
			Ts: t.UTC().Format(time.RFC3339Nano), UUsed: cum[i],
		})
	}
	return out
}

// linearSlope returns OLS slope on (xs, ys) and (true) when defined,
// or (0, false) when n<2 or zero x-variance. Pure helper.
func linearSlope(xs, ys []float64) (float64, bool) {
	n := len(xs)
	if n < 2 {
		return 0, false
	}
	mx, my := mean(xs), mean(ys)
	var num, den float64
	for i := 0; i < n; i++ {
		num += (xs[i] - mx) * (ys[i] - my)
		den += (xs[i] - mx) * (xs[i] - mx)
	}
	if den < noGrowthEps {
		return 0, false
	}
	return num / den, true
}

func mean(xs []float64) float64 {
	var s float64
	for _, v := range xs {
		s += v
	}
	return s / float64(len(xs))
}

func slopeFromBuckets(samples []TimedValue) (float64, bool) {
	if len(samples) < 2 {
		return 0, false
	}
	base := samples[0].T
	xs := make([]float64, len(samples))
	ys := make([]float64, len(samples))
	for i, s := range samples {
		xs[i] = s.T.Sub(base).Seconds() / 86400.0
		ys[i] = s.Value
	}
	return linearSlope(xs, ys)
}

func runwayBand(days float64) string {
	switch {
	case days < 30:
		return "critical"
	case days < 90:
		return "warning"
	default:
		return "healthy"
	}
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round4(v float64) float64 { return math.Round(v*10000) / 10000 }
func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }
