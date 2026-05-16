// Package alerts holds GET handlers for the Python alerts router.
// Currently: alerts list, alert rules list. Deferred: rule get-by-id,
// maintenance-window list/get, ack/resolve actions (writes — Phase 2).
package alerts

import (
	"context"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type Querier interface {
	ListAlerts(ctx context.Context, arg dbq.ListAlertsParams) ([]dbq.Alert, error)
	CountAlerts(ctx context.Context, arg dbq.CountAlertsParams) (int64, error)
	ListAlertRules(ctx context.Context, arg dbq.ListAlertRulesParams) ([]dbq.AlertRule, error)
	CountAlertRules(ctx context.Context, arg dbq.CountAlertRulesParams) (int64, error)
}

type Handler struct {
	Q Querier
}

func (h *Handler) Mount(r chi.Router) {
	r.Route("/alerts", func(r chi.Router) {
		r.Get("/", h.listAlerts)
		r.Get("/rules", h.listRules)
	})
}

type alertsPage struct {
	Items  []dbq.Alert `json:"items"`
	Total  int64       `json:"total"`
	Limit  int32       `json:"limit"`
	Offset int32       `json:"offset"`
}

func (h *Handler) listAlerts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(q.Get("limit"), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
	params := dbq.ListAlertsParams{
		Limit: limit, Offset: offset,
		State: strPtr(q.Get("state")), Severity: strPtr(q.Get("severity")),
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
	limit := parseInt32(q.Get("limit"), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
	params := dbq.ListAlertRulesParams{Limit: limit, Offset: offset}
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
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, rulesPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func parseInt32(s string, def, lo, hi int32) int32 {
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
