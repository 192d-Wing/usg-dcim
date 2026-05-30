package dashboards

import (
	"context"
	"net/http"
	"sort"
	"strconv"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/capacity"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// FreeSpaceQuerier is the slice of dbq methods /free-space needs.
// Distinct from the Querier interface on dashboards.Handler so the
// existing enterprise test fake doesn't have to grow new methods.
type FreeSpaceQuerier interface {
	ListRacksForFreeSpace(ctx context.Context, arg dbq.ListRacksForFreeSpaceParams) ([]dbq.Rack, error)
	ListAssetsByRackIDs(ctx context.Context, rackIDs []uuid.UUID) ([]dbq.Asset, error)
	capacity.Querier
}

// freeSpaceRack is the per-row wire shape — rack identity + the full
// RackCapacity rollup flattened in. Mirrors the Python `{rack_id,
// site_id, code, name, u_height, **cap}` spread.
type freeSpaceRack struct {
	RackID                string             `json:"rack_id"`
	SiteID                string             `json:"site_id"`
	Code                  string             `json:"code"`
	Name                  string             `json:"name"`
	UHeight               int32              `json:"u_height"`
	UUsed                 int32              `json:"u_used"`
	UTotal                int32              `json:"u_total"`
	UPct                  float64            `json:"u_pct"`
	UFree                 int32              `json:"u_free"`
	KwCurrent             *float64           `json:"kw_current"`
	KwMax                 *float64           `json:"kw_max"`
	KwPct                 *float64           `json:"kw_pct"`
	BiggestContiguousFree int32              `json:"biggest_contiguous_free"`
	FreeRuns              []capacity.FreeRun `json:"free_runs"`
}

// freeSpaceResponse mirrors api/dashboards.py's free_space return —
// {query, racks, count}.
type freeSpaceResponse struct {
	Query freeSpaceQuery  `json:"query"`
	Racks []freeSpaceRack `json:"racks"`
	Count int             `json:"count"`
}

type freeSpaceQuery struct {
	MinU          int32    `json:"min_u"`
	SiteID        *string  `json:"site_id"`
	RegionID      *string  `json:"region_id"`
	MinKwHeadroom *float64 `json:"min_kw_headroom"`
}

// freeSpaceHandler reuses the dashboards Handler with its Q widened to
// FreeSpaceQuerier — main.go wires the same *dbq.Queries so it
// satisfies both surfaces. The handler stays method-on-Handler because
// dashboards owns the mount path.
func (h *Handler) freeSpace(w http.ResponseWriter, r *http.Request) {
	q, ok := h.Q.(FreeSpaceQuerier)
	if !ok {
		// Defense-in-depth: the wiring in main.go satisfies this; a test
		// that constructs Handler with a narrower fake would land here.
		httpx.Error(w, http.StatusInternalServerError, "free-space requires full Querier")
		return
	}

	qs := r.URL.Query()
	minU := clampInt32(qs.Get("u"), 1, 0, 60)
	limit := clampInt32(qs.Get("limit"), 50, 1, 500)
	siteFilter := parseUUIDPtr(qs.Get("site_id"))
	regionFilter := parseUUIDPtr(qs.Get("region_id"))
	minKwHeadroom := parseFloatPtr(qs.Get("min_kw_headroom"))

	ctx := r.Context()
	racks, err := q.ListRacksForFreeSpace(ctx, dbq.ListRacksForFreeSpaceParams{
		SiteID:   siteFilter,
		RegionID: regionFilter,
	})
	if err != nil {
		mapErr(w, err)
		return
	}
	if len(racks) == 0 {
		httpx.JSON(w, http.StatusOK, freeSpaceResponse{
			Query: buildFreeSpaceQuery(minU, siteFilter, regionFilter, minKwHeadroom),
			Racks: []freeSpaceRack{},
			Count: 0,
		})
		return
	}

	rackIDs := make([]uuid.UUID, len(racks))
	for i, r := range racks {
		rackIDs[i] = r.ID
	}
	allAssets, err := q.ListAssetsByRackIDs(ctx, rackIDs)
	if err != nil {
		mapErr(w, err)
		return
	}
	assetsByRack := map[uuid.UUID][]dbq.Asset{}
	for _, a := range allAssets {
		if a.RackID == nil {
			continue
		}
		assetsByRack[*a.RackID] = append(assetsByRack[*a.RackID], a)
	}
	caps, err := capacity.ComputeManyRackCapacity(ctx, q, racks, assetsByRack)
	if err != nil {
		mapErr(w, err)
		return
	}

	out := assembleFreeSpace(racks, caps, minU, minKwHeadroom)
	// Sort by biggest_contiguous_free desc, then cap. Stable across
	// ties so the order matches Python (which uses Python's stable
	// sort with a key function — same semantics).
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].BiggestContiguousFree > out[j].BiggestContiguousFree
	})
	if int32(len(out)) > limit {
		out = out[:limit]
	}

	httpx.JSON(w, http.StatusOK, freeSpaceResponse{
		Query: buildFreeSpaceQuery(minU, siteFilter, regionFilter, minKwHeadroom),
		Racks: out,
		Count: len(out),
	})
}

// assembleFreeSpace applies the min_u + min_kw_headroom rejections and
// flattens RackCapacity into the wire row shape. Extracted so the
// SonarLint cognitive-complexity gate stays happy.
func assembleFreeSpace(
	racks []dbq.Rack, caps map[uuid.UUID]capacity.RackCapacity,
	minU int32, minKwHeadroom *float64,
) []freeSpaceRack {
	out := []freeSpaceRack{}
	for _, r := range racks {
		cap := caps[r.ID]
		if cap.BiggestContiguousFree < minU {
			continue
		}
		if minKwHeadroom != nil && cap.KwMax != nil && cap.KwCurrent != nil &&
			(*cap.KwMax-*cap.KwCurrent) < *minKwHeadroom {
			continue
		}
		out = append(out, freeSpaceRack{
			RackID:                r.ID.String(),
			SiteID:                r.SiteID.String(),
			Code:                  r.Code,
			Name:                  r.Name,
			UHeight:               r.UHeight,
			UUsed:                 cap.UUsed,
			UTotal:                cap.UTotal,
			UPct:                  cap.UPct,
			UFree:                 cap.UFree,
			KwCurrent:             cap.KwCurrent,
			KwMax:                 cap.KwMax,
			KwPct:                 cap.KwPct,
			BiggestContiguousFree: cap.BiggestContiguousFree,
			FreeRuns:              cap.FreeRuns,
		})
	}
	return out
}

func buildFreeSpaceQuery(minU int32, site, region *uuid.UUID, minKwHeadroom *float64) freeSpaceQuery {
	q := freeSpaceQuery{MinU: minU, MinKwHeadroom: minKwHeadroom}
	if site != nil {
		s := site.String()
		q.SiteID = &s
	}
	if region != nil {
		s := region.String()
		q.RegionID = &s
	}
	return q
}

// clampInt32 mirrors the FastAPI `Query(<default>, ge=<lo>, le=<hi>)`
// pattern: empty / non-numeric → default; out-of-range → clamped.
func clampInt32(s string, def, lo, hi int32) int32 {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	v := int32(n)
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func parseUUIDPtr(s string) *uuid.UUID {
	if s == "" {
		return nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return nil
	}
	return &id
}

func parseFloatPtr(s string) *float64 {
	if s == "" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}
