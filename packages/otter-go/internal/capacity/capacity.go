// Package capacity holds the rack-capacity rollup primitives — U
// utilization, kW utilization, and contiguous free-U runs. Mirrors
// packages/otter/src/dcim/services/capacity.py byte-for-byte so the
// dashboards endpoints producing this shape (`/free-space`, eventually
// `/racks/{id}` + `/sites/{id}`) stay wire-compatible during the
// phased dashboards port.
package capacity

import (
	"context"
	"sort"
	"strconv"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// Metrics treated as "input kW" for rollup. PDUs that report W instead
// get scaled by 1000. Same buckets as Python's POWER_METRIC_KW /
// POWER_METRIC_W in services/capacity.py.
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

// FreeRun is one contiguous unused U range on a rack.
type FreeRun struct {
	StartU int32 `json:"start_u"`
	Length int32 `json:"length"`
}

// RackCapacity is the dict-shape api/dashboards.py returns for a rack
// rollup. Field tags + ordering match Python.
type RackCapacity struct {
	UUsed                 int32     `json:"u_used"`
	UTotal                int32     `json:"u_total"`
	UPct                  float64   `json:"u_pct"`
	UFree                 int32     `json:"u_free"`
	KwCurrent             *float64  `json:"kw_current"`
	KwMax                 *float64  `json:"kw_max"`
	KwPct                 *float64  `json:"kw_pct"`
	BiggestContiguousFree int32     `json:"biggest_contiguous_free"`
	FreeRuns              []FreeRun `json:"free_runs"`
}

// SlotsUsed builds a 1-indexed boolean array; slot[u]=true if any
// placed asset occupies u. Slot 0 is unused; slot[uHeight+1] is the
// sentinel Python's range() walks past. Mirrors slots_used().
func SlotsUsed(assets []dbq.Asset, uHeight int32) []bool {
	used := make([]bool, uHeight+2)
	for _, a := range assets {
		if a.RackPositionU == nil || *a.RackPositionU < 1 || *a.RackPositionU > uHeight {
			continue
		}
		span := int32(1)
		if a.RackUnits != nil && *a.RackUnits > 1 {
			span = *a.RackUnits
		}
		end := *a.RackPositionU + span
		if end > uHeight+1 {
			end = uHeight + 1
		}
		for u := *a.RackPositionU; u < end; u++ {
			used[u] = true
		}
	}
	return used
}

// FreeRuns returns sorted (longest first, ties broken by start_u)
// contiguous unused U ranges. Mirrors _free_runs().
func FreeRuns(used []bool, uHeight int32) []FreeRun {
	out := []FreeRun{}
	var curStart, curLen int32
	for u := int32(1); u <= uHeight; u++ {
		if !used[u] {
			if curStart == 0 {
				curStart = u
			}
			curLen++
		} else {
			if curStart != 0 {
				out = append(out, FreeRun{StartU: curStart, Length: curLen})
			}
			curStart = 0
			curLen = 0
		}
	}
	if curStart != 0 {
		out = append(out, FreeRun{StartU: curStart, Length: curLen})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Length != out[j].Length {
			return out[i].Length > out[j].Length
		}
		return out[i].StartU < out[j].StartU
	})
	return out
}

// Querier is the slice of dbq methods the rollup helpers need. Kept
// narrow so callers can substitute an in-memory fake in tests.
type Querier interface {
	ListPduKwTelemetry(ctx context.Context, assetIDs []uuid.UUID) ([]dbq.PduKwTelemetryRow, error)
}

// ComputeRackCapacity is the per-rack rollup used by /racks/{id},
// /sites/{id}, and /free-space. Mirrors compute_rack_capacity() in
// services/capacity.py.
//
// One round-trip to telemetry_sources per rack would be expensive on
// the /free-space + /sites/{id} fan-outs; the *Bulk variant below
// keeps the lookup to a single ListPduKwTelemetry call.
func ComputeRackCapacity(ctx context.Context, q Querier, rack dbq.Rack, assets []dbq.Asset) (RackCapacity, error) {
	pduIDs := pduAssetIDs(assets)
	pduRows, err := q.ListPduKwTelemetry(ctx, pduIDs)
	if err != nil {
		return RackCapacity{}, err
	}
	return assembleRackCapacity(rack, assets, pduRows), nil
}

// ComputeManyRackCapacity is the fan-out variant: one bulk telemetry
// lookup, then per-rack assembly using only the rows that matched the
// rack's PDUs. Saves N round-trips on /free-space + /sites/{id}.
func ComputeManyRackCapacity(
	ctx context.Context, q Querier,
	racks []dbq.Rack, assetsByRack map[uuid.UUID][]dbq.Asset,
) (map[uuid.UUID]RackCapacity, error) {
	allPduIDs := []uuid.UUID{}
	seen := map[uuid.UUID]struct{}{}
	for _, assets := range assetsByRack {
		for _, id := range pduAssetIDs(assets) {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			allPduIDs = append(allPduIDs, id)
		}
	}
	rowsByAsset := map[uuid.UUID][]dbq.PduKwTelemetryRow{}
	if len(allPduIDs) > 0 {
		rows, err := q.ListPduKwTelemetry(ctx, allPduIDs)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			rowsByAsset[r.AssetID] = append(rowsByAsset[r.AssetID], r)
		}
	}
	out := make(map[uuid.UUID]RackCapacity, len(racks))
	for _, r := range racks {
		assets := assetsByRack[r.ID]
		// Slice the bulk telemetry down to rows for this rack's PDUs.
		var rackRows []dbq.PduKwTelemetryRow
		for _, id := range pduAssetIDs(assets) {
			rackRows = append(rackRows, rowsByAsset[id]...)
		}
		out[r.ID] = assembleRackCapacity(r, assets, rackRows)
	}
	return out, nil
}

func pduAssetIDs(assets []dbq.Asset) []uuid.UUID {
	out := []uuid.UUID{}
	for _, a := range assets {
		if a.Kind == "pdu" {
			out = append(out, a.ID)
		}
	}
	return out
}

func assembleRackCapacity(
	rack dbq.Rack, assets []dbq.Asset, pduRows []dbq.PduKwTelemetryRow,
) RackCapacity {
	used := SlotsUsed(assets, rack.UHeight)
	var uUsed int32
	for u := int32(1); u <= rack.UHeight; u++ {
		if used[u] {
			uUsed++
		}
	}
	runs := FreeRuns(used, rack.UHeight)
	uPct := 0.0
	if rack.UHeight > 0 {
		uPct = round1(100.0 * float64(uUsed) / float64(rack.UHeight))
	}
	kwCurrent := rollupKw(pduRows)
	kwMax := parseFloatPtr(rack.MaxKw)
	cap := RackCapacity{
		UUsed:                 uUsed,
		UTotal:                rack.UHeight,
		UPct:                  uPct,
		UFree:                 rack.UHeight - uUsed,
		KwCurrent:             kwCurrent,
		KwMax:                 kwMax,
		BiggestContiguousFree: 0,
		FreeRuns:              capRuns(runs, 8),
	}
	if len(runs) > 0 {
		cap.BiggestContiguousFree = runs[0].Length
	}
	if kwCurrent != nil && kwMax != nil && *kwMax > 0 {
		v := round1(100.0 * *kwCurrent / *kwMax)
		cap.KwPct = &v
	}
	return cap
}

// rollupKw sums the kW/W metric values across the PDU telemetry rows.
// Returns nil when no row carried a parseable value (Python returns
// None in the same case).
func rollupKw(rows []dbq.PduKwTelemetryRow) *float64 {
	var total float64
	any := false
	for _, r := range rows {
		if r.LastValue == nil {
			continue
		}
		v, err := strconv.ParseFloat(*r.LastValue, 64)
		if err != nil {
			continue
		}
		if _, ok := powerMetricKw[r.Metric]; ok {
			total += v
			any = true
			continue
		}
		if _, ok := powerMetricW[r.Metric]; ok {
			total += v / 1000.0
			any = true
		}
	}
	if !any {
		return nil
	}
	out := round3(total)
	return &out
}

// parseFloatPtr converts a NUMERIC-as-text pointer to *float64, returning
// nil for nil input or unparseable text. PG NUMERIC scans cleanly into
// *string in pgx; conversion to float happens at the rollup boundary.
func parseFloatPtr(s *string) *float64 {
	if s == nil {
		return nil
	}
	v, err := strconv.ParseFloat(*s, 64)
	if err != nil {
		return nil
	}
	return &v
}

func capRuns(runs []FreeRun, max int) []FreeRun {
	if len(runs) > max {
		return runs[:max]
	}
	return runs
}

// round1 / round3: same rounding behavior Python's round(v, 1) and
// round(v, 3) use. JSON encoding emits the minimal decimal form for
// both sides — finch parses floats with parseFloat regardless.
func round1(v float64) float64 {
	return floatRound(v, 10)
}
func round3(v float64) float64 {
	return floatRound(v, 1000)
}
func floatRound(v, scale float64) float64 {
	if v >= 0 {
		return float64(int64(v*scale+0.5)) / scale
	}
	return float64(int64(v*scale-0.5)) / scale
}
