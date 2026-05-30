// Package dashboards holds enterprise + per-site dashboard endpoints.
// Shipped so far: enterprise overview (Phase 1), free-space (Phase 2
// — uses internal/capacity), sites/at-risk (Phase 2b — alert_severity
// ENUM >= compare). Remaining endpoints (racks/{id}, assets/{id},
// forecasts, sites/{id}) stay on Python until the rest of the
// services/ helpers (power_chain, forecast, and the sites topology
// joins) get ported.
package dashboards

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type Querier interface {
	// Shared with sites/racks handlers — zero-value params struct →
	// global unscoped count; LifecycleState pointer → active-only.
	CountSites(ctx context.Context, arg dbq.CountSitesParams) (int64, error)
	CountRacks(ctx context.Context, arg dbq.CountRacksParams) (int64, error)

	// Dashboard-specific aggregates.
	CountSitesWithCriticalAlerts(ctx context.Context) (int64, error)
	CountHealthyCollectors(ctx context.Context) (int64, error)
	CountStaleCollectors(ctx context.Context, staleBefore time.Time) (int64, error)
	CountStaleTelemetrySources(ctx context.Context) (int64, error)
}

type Handler struct {
	Q Querier
	// CollectorStaleSeconds mirrors Python's
	// settings.collector_stale_seconds (default 600). A collector is
	// "stale" when its last_seen_at is older than now - this value
	// or is null while the collector is enabled. Wired from main.go
	// via DCIM_COLLECTOR_STALE_SECONDS.
	CollectorStaleSeconds int
}

// capDashboardsRead is the cap every /dashboards route gates on.
// Extracted so the linter doesn't flag the string-literal duplication.
const capDashboardsRead = "dashboards:dashboards:read"

func (h *Handler) Mount(r chi.Router) {
	r.With(auth.RequireCapability(capDashboardsRead)).Get("/dashboards/enterprise", h.enterprise)
	r.With(auth.RequireCapability(capDashboardsRead)).Get("/dashboards/free-space", h.freeSpace)
	r.With(auth.RequireCapability(capDashboardsRead)).Get("/dashboards/sites/at-risk", h.sitesAtRisk)
}

// enterpriseOverview is the wire shape returned by GET
// /api/v1/dashboards/enterprise. Mirrors Python's
// enterprise_overview() return dict byte-for-byte (same keys, same
// nesting, same value types — int64 because Postgres COUNT returns
// bigint).
type enterpriseOverview struct {
	Sites       sitesKpi      `json:"sites"`
	Racks       racksKpi      `json:"racks"`
	Alerts      alertsKpi     `json:"alerts"`
	Collectors  collectorsKpi `json:"collectors"`
	Telemetry   telemetryKpi  `json:"telemetry"`
	GeneratedAt string        `json:"generated_at"`
}

type sitesKpi struct {
	Total  int64 `json:"total"`
	Active int64 `json:"active"`
}
type racksKpi struct {
	Total int64 `json:"total"`
}
type alertsKpi struct {
	SitesWithCritical int64 `json:"sites_with_critical"`
}
type collectorsKpi struct {
	Healthy int64 `json:"healthy"`
	Stale   int64 `json:"stale"`
}
type telemetryKpi struct {
	StaleSources int64 `json:"stale_sources"`
}

func (h *Handler) enterprise(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := time.Now().UTC()
	staleBefore := now.Add(-time.Duration(h.CollectorStaleSeconds) * time.Second)

	siteTotal, err := h.Q.CountSites(ctx, dbq.CountSitesParams{})
	if err != nil {
		mapErr(w, err)
		return
	}
	activeState := "active"
	siteActive, err := h.Q.CountSites(ctx, dbq.CountSitesParams{LifecycleState: &activeState})
	if err != nil {
		mapErr(w, err)
		return
	}
	rackTotal, err := h.Q.CountRacks(ctx, dbq.CountRacksParams{})
	if err != nil {
		mapErr(w, err)
		return
	}
	sitesWithCritical, err := h.Q.CountSitesWithCriticalAlerts(ctx)
	if err != nil {
		mapErr(w, err)
		return
	}
	healthyCollectors, err := h.Q.CountHealthyCollectors(ctx)
	if err != nil {
		mapErr(w, err)
		return
	}
	staleCollectors, err := h.Q.CountStaleCollectors(ctx, staleBefore)
	if err != nil {
		mapErr(w, err)
		return
	}
	staleSources, err := h.Q.CountStaleTelemetrySources(ctx)
	if err != nil {
		mapErr(w, err)
		return
	}

	httpx.JSON(w, http.StatusOK, enterpriseOverview{
		Sites:       sitesKpi{Total: siteTotal, Active: siteActive},
		Racks:       racksKpi{Total: rackTotal},
		Alerts:      alertsKpi{SitesWithCritical: sitesWithCritical},
		Collectors:  collectorsKpi{Healthy: healthyCollectors, Stale: staleCollectors},
		Telemetry:   telemetryKpi{StaleSources: staleSources},
		GeneratedAt: now.Format(time.RFC3339Nano),
	})
}

func mapErr(w http.ResponseWriter, err error) {
	status, msg := httpx.Mapped(err)
	httpx.Error(w, status, msg)
}
