// Package alerts holds the CRUD handlers for the alerts module:
// alerts list + ack, alert_rules CRUD, maintenance_windows CRUD. The
// arq-driven evaluation loop in packages/otter/.../services/alerts.py
// is still Python until the Go scheduler lands; only the HTTP routes
// live here. Python's api/alerts.py is gone since the alerts cutover.
package alerts

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type Querier interface {
	ListAlerts(ctx context.Context, arg dbq.ListAlertsParams) ([]dbq.Alert, error)
	CountAlerts(ctx context.Context, arg dbq.CountAlertsParams) (int64, error)
	ListAlertRules(ctx context.Context, arg dbq.ListAlertRulesParams) ([]dbq.AlertRule, error)
	CountAlertRules(ctx context.Context, arg dbq.CountAlertRulesParams) (int64, error)
	GetAlertRule(ctx context.Context, id uuid.UUID) (dbq.AlertRule, error)
	ListMaintenanceWindows(ctx context.Context, arg dbq.ListMaintenanceWindowsParams) ([]dbq.MaintenanceWindow, error)
	CountMaintenanceWindows(ctx context.Context, arg dbq.CountMaintenanceWindowsParams) (int64, error)
	GetMaintenanceWindow(ctx context.Context, id uuid.UUID) (dbq.MaintenanceWindow, error)

	// Mutations (PR 45)
	AckAlert(ctx context.Context, arg dbq.AckAlertParams) (dbq.Alert, error)
	CreateAlertRule(ctx context.Context, arg dbq.CreateAlertRuleParams) (dbq.AlertRule, error)
	UpdateAlertRule(ctx context.Context, arg dbq.UpdateAlertRuleParams) (dbq.AlertRule, error)
	DeleteAlertRule(ctx context.Context, id uuid.UUID) error
	CreateMaintenanceWindow(ctx context.Context, arg dbq.CreateMaintenanceWindowParams) (dbq.MaintenanceWindow, error)
	UpdateMaintenanceWindow(ctx context.Context, arg dbq.UpdateMaintenanceWindowParams) (dbq.MaintenanceWindow, error)
	DeleteMaintenanceWindow(ctx context.Context, id uuid.UUID) error

	// PR 59 — site-scope ABAC. site_scope_id / site_id are nullable;
	// nil = enterprise-default and only global principals can mutate.
	// GetSiteRegionID + ListSiteGroupIDsForSite expose the region +
	// site-group expansion EnforceSiteScope needs (same shape used by
	// sites/racks/assets handlers).
	GetAlertRuleSiteScopeID(ctx context.Context, id uuid.UUID) (*uuid.UUID, error)
	GetMaintenanceWindowSiteID(ctx context.Context, id uuid.UUID) (*uuid.UUID, error)
	GetSiteRegionID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetSiteOrganizationID(ctx context.Context, id uuid.UUID) (*uuid.UUID, error)
	ListSiteGroupIDsForSite(ctx context.Context, siteID uuid.UUID) ([]uuid.UUID, error)

	// PR 63: scope-filtered LISTs. Site-scope expansion to a concrete
	// site_id set; powers the ScopeSiteIds filter on alerts /
	// alert_rules / maintenance_windows. The nullable-site "enterprise
	// default" semantic — site_scope_id / site_id IS NULL means
	// "applies to every site" — is handled in the SQL (rules + windows
	// match NULL in addition to the set; alerts.site_id is NOT NULL so
	// it's the standard shape).
	ListSiteIDsForExpansion(ctx context.Context, arg dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error)
}

type Handler struct {
	Q     Querier
	Audit audit.Recorder
}

func (h *Handler) Mount(r chi.Router) {
	// Cap codes match the canonical catalog
	// (packages/otter-go/internal/admin/capabilities.go):
	//   alerts:alerts:{read,ack}   ← previously used :update for ack
	//   alerts:rules:{read,create,update,delete}
	//   maintenance:windows:{read,create,update,delete}  ← previously
	//     used `alerts:maintenance-windows:*`, which is not in the
	//     catalog and silently broke role-picker assignments.
	// Same class of RBAC bug as PR #202 (notifications). Read paths
	// are gated explicitly — alerts aren't site-rooted so an
	// ungated GET would leak rule/window/firing-alert metadata to
	// any logged-in user. Mirrors PR #195/#205's fix.
	r.With(auth.RequireCapability(capAlertsRead)).Get("/alerts", h.listAlerts)
	r.With(auth.RequireCapability(capAlertsRead)).Get("/alerts/", h.listAlerts)
	r.With(auth.RequireCapability(capRulesRead)).Get("/alerts/rules", h.listRules)
	r.With(auth.RequireCapability(capRulesRead)).Get(pathRuleByID, h.getRule)
	r.With(auth.RequireCapability(capMWRead)).Get("/alerts/maintenance-windows", h.listMaintenanceWindows)
	r.With(auth.RequireCapability(capMWRead)).Get(pathMWByID, h.getMaintenanceWindow)

	r.With(auth.RequireCapability("alerts:alerts:ack")).Post("/alerts/{id}/ack", h.ack)
	r.With(auth.RequireCapability("alerts:rules:create")).Post("/alerts/rules", h.createRule)
	r.With(auth.RequireCapability("alerts:rules:update")).Patch(pathRuleByID, h.updateRule)
	r.With(auth.RequireCapability("alerts:rules:delete")).Delete(pathRuleByID, h.deleteRule)
	r.With(auth.RequireCapability("maintenance:windows:create")).Post("/alerts/maintenance-windows", h.createMW)
	r.With(auth.RequireCapability("maintenance:windows:update")).Patch(pathMWByID, h.updateMW)
	r.With(auth.RequireCapability("maintenance:windows:delete")).Delete(pathMWByID, h.deleteMW)
}

const (
	capAlertsRead = "alerts:alerts:read"
	capRulesRead  = "alerts:rules:read"
	capMWRead     = "maintenance:windows:read"

	pathRuleByID = "/alerts/rules/{id}"
	pathMWByID   = "/alerts/maintenance-windows/{id}"
)

// alertsPage is the wire shape for /alerts. Alias of httpx.Page[T] so
// the {Items, Total, Limit, Offset} layout stays in lockstep with
// every other paginated handler and the empty-page short-circuit
// path picks up the non-nil empty Items invariant from
// httpx.EmptyPage.
type alertsPage = httpx.Page[dbq.Alert]

func (h *Handler) listAlerts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	p, _ := auth.From(r.Context())
	scopeSiteIds, scoped, err := auth.ScopedSiteFilter(r.Context(), h.Q, p, capAlertsRead)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if scoped && len(scopeSiteIds) == 0 {
		// EmptyPage[dbq.Alert] forces Items=[]Alert{} so the JSON
		// payload is `"items": []`, not `"items": null` — finch's
		// data.items.map() can't survive null.
		httpx.JSON(w, http.StatusOK, httpx.EmptyPage[dbq.Alert](limit, offset))
		return
	}
	params := dbq.ListAlertsParams{
		Limit: limit, Offset: offset,
		State: strPtr(q.Get("state")), Severity: strPtr(q.Get("severity")),
		ScopeSiteIds: scopeSiteIds,
	}
	if v := q.Get("site_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "site_id is not a uuid")
			return
		}
		params.SiteID = &id
	}
	items, err := h.Q.ListAlerts(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountAlerts(r.Context(), dbq.CountAlertsParams{
		SiteID: params.SiteID, State: params.State, Severity: params.Severity,
		ScopeSiteIds: scopeSiteIds,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, alertsPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

type rulesPage struct {
	Items  []dbq.AlertRule `json:"items"`
	Total  int64           `json:"total"`
	Limit  int32           `json:"limit"`
	Offset int32           `json:"offset"`
}

func (h *Handler) listRules(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	// PR 63 — alert_rules.site_scope_id is nullable; NULL = enterprise-
	// default rule (applies to every site, visible to all scoped admins).
	// Pass the slice through unconditionally — even an empty allowed
	// set still surfaces enterprise defaults via the SQL's NULL clause.
	// No short-circuit here.
	p, _ := auth.From(r.Context())
	scopeSiteIds, _, err := auth.ScopedSiteFilter(r.Context(), h.Q, p, capRulesRead)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	params := dbq.ListAlertRulesParams{Limit: limit, Offset: offset, ScopeSiteIds: scopeSiteIds}
	if v := q.Get("site_scope_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "site_scope_id is not a uuid")
			return
		}
		params.SiteScopeID = &id
	}
	if v := q.Get("enabled"); v != "" {
		b := v == "true" || v == "1"
		params.Enabled = &b
	}
	items, err := h.Q.ListAlertRules(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountAlertRules(r.Context(), dbq.CountAlertRulesParams{
		SiteScopeID: params.SiteScopeID, Enabled: params.Enabled,
		ScopeSiteIds: scopeSiteIds,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, rulesPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

// ---- Rule get-by-id ----

func (h *Handler) getRule(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	rule, err := h.Q.GetAlertRule(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "rule not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, rule)
}

// ---- Maintenance windows ----

type maintenanceWindowsPage struct {
	Items  []dbq.MaintenanceWindow `json:"items"`
	Total  int64                   `json:"total"`
	Limit  int32                   `json:"limit"`
	Offset int32                   `json:"offset"`
}

func (h *Handler) listMaintenanceWindows(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	// PR 63 — maintenance_windows.site_id is nullable (same enterprise-
	// default semantic as alert_rules above). Don't short-circuit on
	// empty scope set.
	p, _ := auth.From(r.Context())
	scopeSiteIds, _, err := auth.ScopedSiteFilter(r.Context(), h.Q, p, capMWRead)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	params := dbq.ListMaintenanceWindowsParams{Limit: limit, Offset: offset, ScopeSiteIds: scopeSiteIds}

	if v := q.Get("site_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "site_id is not a uuid")
			return
		}
		params.SiteID = &id
	}
	if v := q.Get("active_at"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "active_at is not RFC3339")
			return
		}
		params.ActiveAt = &t
	}
	// `upcoming=true` matches the Python flag — translated to a
	// server-side `ends_at >= now()` lower-bound so the filter is
	// stable across paginated requests within the same call.
	if v := q.Get("upcoming"); v == "true" || v == "1" {
		now := time.Now().UTC()
		params.UpcomingAfter = &now
	}

	items, err := h.Q.ListMaintenanceWindows(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountMaintenanceWindows(r.Context(), dbq.CountMaintenanceWindowsParams{
		SiteID: params.SiteID, ActiveAt: params.ActiveAt, UpcomingAfter: params.UpcomingAfter,
		ScopeSiteIds: scopeSiteIds,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, maintenanceWindowsPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

func (h *Handler) getMaintenanceWindow(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	mw, err := h.Q.GetMaintenanceWindow(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "maintenance window not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, mw)
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
