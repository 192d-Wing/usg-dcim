// Package telemetry holds the /api/v1/telemetry/series read endpoint.
// One-shot window query against the TimescaleDB hypertable. Mirrors
// the FastAPI handler at packages/otter/src/dcim/api/telemetry.py so
// finch's chart code reads the same wire shape.
package telemetry

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type Querier interface {
	GetTelemetrySeries(ctx context.Context, arg dbq.GetTelemetrySeriesParams) ([]dbq.TelemetryPoint, error)
}

type Handler struct{ Q Querier }

func (h *Handler) Mount(r chi.Router) {
	r.Route("/telemetry", func(r chi.Router) {
		// Python (api/telemetry.py:get_series) gates on
		// `telemetry:metrics:read`; mirror that here so the cutover
		// from Python to Go doesn't widen access.
		r.With(auth.RequireCapability("telemetry:metrics:read")).Get("/series", h.getSeries)
	})
}

type seriesPoint struct {
	TS    string  `json:"ts"`
	Value float64 `json:"value"`
}

type seriesResponse struct {
	AssetID string        `json:"asset_id"`
	Metric  string        `json:"metric"`
	Points  []seriesPoint `json:"points"`
	Count   int           `json:"count"`
}

func (h *Handler) getSeries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	siteID, err := uuid.Parse(q.Get("site_id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "site_id is required and must be a uuid")
		return
	}
	assetID, err := uuid.Parse(q.Get("asset_id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "asset_id is required and must be a uuid")
		return
	}
	metric := q.Get("metric")
	if metric == "" {
		httpx.Error(w, http.StatusBadRequest, "metric is required")
		return
	}

	// Default end=now, start=end-1h to match the FastAPI handler.
	end, err := parseTimeOr(q.Get("end"), time.Now().UTC())
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "end is not a valid timestamp")
		return
	}
	start, err := parseTimeOr(q.Get("start"), end.Add(-1*time.Hour))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "start is not a valid timestamp")
		return
	}

	rows, err := h.Q.GetTelemetrySeries(r.Context(), dbq.GetTelemetrySeriesParams{
		SiteID: siteID, AssetID: assetID, Metric: metric, Start: start, End: end,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	points := make([]seriesPoint, 0, len(rows))
	for _, p := range rows {
		points = append(points, seriesPoint{TS: p.TS.Format(time.RFC3339Nano), Value: p.Value})
	}
	httpx.JSON(w, http.StatusOK, seriesResponse{
		AssetID: assetID.String(), Metric: metric, Points: points, Count: len(points),
	})
}

func parseTimeOr(s string, def time.Time) (time.Time, error) {
	if s == "" {
		return def, nil
	}
	// FastAPI accepts RFC3339 / ISO 8601. time.RFC3339Nano covers both.
	return time.Parse(time.RFC3339Nano, s)
}
