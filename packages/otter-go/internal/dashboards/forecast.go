package dashboards

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/forecast"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// errForecastNarrowQuerier is the 500 message the 3 forecast handlers
// return when h.Q can't be type-asserted to the full surface. Surfaces
// only when a unit test builds Handler with a narrower fake.
const errForecastNarrowQuerier = "forecast requires full Querier"

// ForecastQuerier is the dbq slice the 3 forecast endpoints need.
type ForecastQuerier interface {
	GetRack(ctx context.Context, id uuid.UUID) (dbq.Rack, error)
	GetSite(ctx context.Context, id uuid.UUID) (dbq.Site, error)
	ListAssetsByRackOrdered(ctx context.Context, rackID uuid.UUID) ([]dbq.Asset, error)
	ListRacksForForecast(ctx context.Context, arg dbq.ListRacksForForecastParams) ([]dbq.Rack, error)
	ListAssetsByRackIDs(ctx context.Context, rackIDs []uuid.UUID) ([]dbq.Asset, error)
	ListAssetsBySite(ctx context.Context, siteID uuid.UUID) ([]dbq.Asset, error)
	ListRacksBySite(ctx context.Context, siteID uuid.UUID) ([]dbq.Rack, error)
	forecast.KwHistoryQuerier
}

// racksForecastBatchResponse is the {racks:[...]} shape.
type racksForecastBatchResponse struct {
	Racks []forecast.RackForecast `json:"racks"`
}

// rackForecastResponse is compute_rack_forecast + kw_forecast embed.
// kw_forecast is *KwForecast so the missing-PDU case encodes as null
// (Python returns None when the rack has no PDUs).
type rackForecastResponse struct {
	forecast.RackForecast
	KwForecast *forecast.KwForecast `json:"kw_forecast"`
}

// rackWhatIfResponse mirrors compute_what_if's flattened shape +
// kw_forecast.
type rackWhatIfResponse struct {
	forecast.WhatIfForecast
	KwForecast *forecast.KwForecast `json:"kw_forecast"`
}

func (h *Handler) racksForecastBatch(w http.ResponseWriter, r *http.Request) {
	q, ok := h.Q.(ForecastQuerier)
	if !ok {
		httpx.Error(w, http.StatusInternalServerError, errForecastNarrowQuerier)
		return
	}
	qs := r.URL.Query()
	limit := clampInt32(qs.Get("limit"), 200, 1, 1000)
	siteFilter := parseUUIDPtr(qs.Get("site_id"))
	ctx := r.Context()
	now := time.Now().UTC()

	racks, err := q.ListRacksForForecast(ctx, dbq.ListRacksForForecastParams{
		SiteID: siteFilter, Limit: limit,
	})
	if err != nil {
		mapErr(w, err)
		return
	}
	if len(racks) == 0 {
		httpx.JSON(w, http.StatusOK, racksForecastBatchResponse{Racks: []forecast.RackForecast{}})
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
	byRack := map[uuid.UUID][]dbq.Asset{}
	for _, a := range allAssets {
		if a.RackID == nil {
			continue
		}
		byRack[*a.RackID] = append(byRack[*a.RackID], a)
	}
	out := make([]forecast.RackForecast, 0, len(racks))
	for _, r := range racks {
		f := forecast.ComputeRackForecast(r, byRack[r.ID], now)
		// Batch caller strips history to keep the payload tight.
		f.History = nil
		out = append(out, f)
	}
	httpx.JSON(w, http.StatusOK, racksForecastBatchResponse{Racks: out})
}

func (h *Handler) rackForecast(w http.ResponseWriter, r *http.Request) {
	q, ok := h.Q.(ForecastQuerier)
	if !ok {
		httpx.Error(w, http.StatusInternalServerError, errForecastNarrowQuerier)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "rack_id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "rack_id is not a uuid")
		return
	}
	qs := r.URL.Query()
	addUnits := clampInt32(qs.Get("add_units"), 0, 0, 60)
	kwDays := clampInt32(qs.Get("kw_days"), 90, 7, 365)

	ctx := r.Context()
	rack, err := q.GetRack(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.JSON(w, http.StatusOK, notFoundResponse{Error: "not_found"})
			return
		}
		mapErr(w, err)
		return
	}
	assets, err := q.ListAssetsByRackOrdered(ctx, id)
	if err != nil {
		mapErr(w, err)
		return
	}
	now := time.Now().UTC()

	kw, err := loadKwForecast(ctx, q, rack, assets, kwDays, now)
	if err != nil {
		mapErr(w, err)
		return
	}

	if addUnits > 0 {
		wf := forecast.ComputeWhatIf(rack, assets, addUnits, now)
		httpx.JSON(w, http.StatusOK, rackWhatIfResponse{WhatIfForecast: wf, KwForecast: kw})
		return
	}
	rf := forecast.ComputeRackForecast(rack, assets, now)
	httpx.JSON(w, http.StatusOK, rackForecastResponse{RackForecast: rf, KwForecast: kw})
}

// loadKwForecast wraps the PDU id collection + telemetry fetch + the
// pure projection. Returns (nil, nil) when the rack has no PDUs —
// Python returns None on that branch and finch renders "no PDU
// telemetry available" rather than the no-trend variant.
func loadKwForecast(
	ctx context.Context, q ForecastQuerier, rack dbq.Rack, assets []dbq.Asset,
	days int32, now time.Time,
) (*forecast.KwForecast, error) {
	var pduIDs []uuid.UUID
	for _, a := range assets {
		if a.Kind == "pdu" {
			pduIDs = append(pduIDs, a.ID)
		}
	}
	if len(pduIDs) == 0 {
		return nil, nil
	}
	start := now.Add(-time.Duration(days) * 24 * time.Hour)
	rows, err := forecast.FetchKwHistory(ctx, q, pduIDs, start, now)
	if err != nil {
		// Hypertable unreachable → degrade so the U-only forecast
		// still renders. Mirrors Python's SQLAlchemyError branch.
		kw := forecast.ProjectKw(nil, parseFloat(rack.MaxKw), days, now)
		return &kw, nil
	}
	samples := forecast.SamplesFromRows(rows)
	kw := forecast.ProjectKw(samples, parseFloat(rack.MaxKw), days, now)
	return &kw, nil
}

func (h *Handler) siteForecast(w http.ResponseWriter, r *http.Request) {
	q, ok := h.Q.(ForecastQuerier)
	if !ok {
		httpx.Error(w, http.StatusInternalServerError, errForecastNarrowQuerier)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "site_id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "site_id is not a uuid")
		return
	}
	ctx := r.Context()
	if _, err := q.GetSite(ctx, id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.JSON(w, http.StatusOK, notFoundResponse{Error: "not_found"})
			return
		}
		mapErr(w, err)
		return
	}
	racks, err := q.ListRacksBySite(ctx, id)
	if err != nil {
		mapErr(w, err)
		return
	}
	assets, err := q.ListAssetsBySite(ctx, id)
	if err != nil {
		mapErr(w, err)
		return
	}
	byRack := map[uuid.UUID][]dbq.Asset{}
	for _, a := range assets {
		if a.RackID == nil {
			continue
		}
		byRack[*a.RackID] = append(byRack[*a.RackID], a)
	}
	now := time.Now().UTC()
	out := forecast.ComputeSiteForecast(id, racks, byRack, now)
	httpx.JSON(w, http.StatusOK, out)
}
