// Alerts mutations (PR 45). ack action, rules CRUD, maintenance
// windows CRUD. Resolve + comment endpoints don't exist in Python's
// alerts router (alerts auto-resolve when the underlying condition
// clears) so they aren't ported.
//
// ABAC (PR 59): both alert_rules.site_scope_id and
// maintenance_windows.site_id are NULLABLE. A nil value means
// "enterprise default" (applies to every site); only a global
// principal can mutate. A scoped principal touching a non-nil
// resource walks the usual region + site-group expansion through
// auth.EnforceSiteScope. Update paths also enforce on the new site
// when the caller is reassigning, so a site-X admin can't elevate
// a rule into an enterprise default or into a site they don't own.
package alerts

import (
	"context"
	"encoding/json"
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

// enforceSiteOrGlobal is the PR 59 nullable-site-scope guard. alert_rules
// and maintenance_windows both carry a nullable site (site_scope_id /
// site_id). The semantics:
//
//   - siteID == nil  → "enterprise default" (applies to every site).
//     Only a global principal can mutate. A scoped principal gets 403
//     even if they hold the cap, because they can't unilaterally
//     create/touch a rule that affects sites outside their scope.
//   - siteID != nil  → standard EnforceSiteScope; DB-backed region +
//     site-group expansion through Handler.Q.
//
// Returns false when a response has been written.
func (h *Handler) enforceSiteOrGlobal(w http.ResponseWriter, r *http.Request, siteID *uuid.UUID, capCode string) bool {
	p, _ := auth.From(r.Context())
	if siteID == nil {
		s := auth.FindScope(p, capCode)
		if s != nil && s.IsGlobal {
			return true
		}
		httpx.Error(w, http.StatusForbidden, "enterprise-default rule: requires global scope")
		return false
	}
	if err := auth.EnforceSiteScope(r.Context(), h.Q, p, *siteID, capCode); err != nil {
		httpx.Error(w, http.StatusForbidden, err.Error())
		return false
	}
	return true
}

// lookupNullableSiteID runs a slim nullable-site lookup and maps
// pgx.ErrNoRows to a 404 with notFoundMsg. ok=false means a response
// was already written. The returned pointer may be nil (enterprise
// default) — callers pass it straight into enforceSiteOrGlobal.
func (h *Handler) lookupNullableSiteID(w http.ResponseWriter, ctx context.Context, fn func(context.Context) (*uuid.UUID, error), notFoundMsg string) (*uuid.UUID, bool) {
	sid, err := fn(ctx)
	if err != nil {
		mapErr(w, err, notFoundMsg)
		return nil, false
	}
	return sid, true
}

func idFromURL(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return uuid.Nil, false
	}
	return id, true
}

func mapErr(w http.ResponseWriter, err error, notFoundMsg string) {
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, notFoundMsg)
		return
	}
	status, msg := httpx.Mapped(err)
	httpx.Error(w, status, msg)
}

// ---- Ack ----

type ackReq struct {
	Note *string `json:"note"`
}

func (h *Handler) ack(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	var req ackReq
	_ = json.NewDecoder(r.Body).Decode(&req) // body optional
	p, _ := auth.From(r.Context())
	ackedBy := p.Label
	if ackedBy == "" {
		ackedBy = "unknown"
	}
	out, err := h.Q.AckAlert(r.Context(), dbq.AckAlertParams{ID: id, AckedBy: ackedBy})
	if err != nil {
		mapErr(w, err, "alert not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "alert.ack", TargetType: "alert", TargetID: id.String()})
	httpx.JSON(w, http.StatusOK, out)
}

// ---- Alert rules ----

type ruleCreateReq struct {
	Name            string          `json:"name"`
	Description     *string         `json:"description"`
	Metric          string          `json:"metric"`
	Operator        string          `json:"operator"`
	Threshold       float64         `json:"threshold"`
	DurationSeconds *int32          `json:"duration_seconds"`
	Severity        string          `json:"severity"`
	SiteScopeID     *uuid.UUID      `json:"site_scope_id"`
	AssetFilterJson json.RawMessage `json:"asset_filter_json"`
	Enabled         *bool           `json:"enabled"`
	RunbookURL      *string         `json:"runbook_url"`
}

func (h *Handler) createRule(w http.ResponseWriter, r *http.Request) {
	var req ruleCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.Name == "" || req.Metric == "" || req.Operator == "" || req.Severity == "" {
		httpx.Error(w, http.StatusBadRequest, "name, metric, operator, severity required")
		return
	}
	if !h.enforceSiteOrGlobal(w, r, req.SiteScopeID, "alerts:rules:create") {
		return
	}
	duration := int32(60)
	if req.DurationSeconds != nil {
		duration = *req.DurationSeconds
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	out, err := h.Q.CreateAlertRule(r.Context(), dbq.CreateAlertRuleParams{
		Name: req.Name, Description: req.Description,
		Metric: req.Metric, Operator: req.Operator, Threshold: req.Threshold,
		DurationSeconds: duration, Severity: req.Severity,
		SiteScopeID: req.SiteScopeID, AssetFilterJson: req.AssetFilterJson,
		Enabled: enabled, RunbookURL: req.RunbookURL,
	})
	if err != nil {
		mapErr(w, err, "rule not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "alert_rule.create", TargetType: "alert_rule", TargetID: out.ID.String()})
	httpx.JSON(w, http.StatusCreated, out)
}

type ruleUpdateReq struct {
	Name            *string
	Description     *string
	descriptionSet  bool
	Metric          *string
	Operator        *string
	Threshold       *float64
	DurationSeconds *int32
	Severity        *string
	SiteScopeID     *uuid.UUID
	siteSet         bool
	AssetFilterJson json.RawMessage
	Enabled         *bool
	RunbookURL      *string
	runbookSet      bool
}

func (u *ruleUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for k, dst := range map[string]any{
		"name": &u.Name, "metric": &u.Metric, "operator": &u.Operator,
		"threshold": &u.Threshold, "duration_seconds": &u.DurationSeconds,
		"severity": &u.Severity, "enabled": &u.Enabled,
	} {
		if v, ok := raw[k]; ok {
			_ = json.Unmarshal(v, dst)
		}
	}
	if v, ok := raw["description"]; ok {
		u.descriptionSet = true
		_ = json.Unmarshal(v, &u.Description)
	}
	if v, ok := raw["site_scope_id"]; ok {
		u.siteSet = true
		_ = json.Unmarshal(v, &u.SiteScopeID)
	}
	if v, ok := raw["asset_filter_json"]; ok {
		u.AssetFilterJson = v
	}
	if v, ok := raw["runbook_url"]; ok {
		u.runbookSet = true
		_ = json.Unmarshal(v, &u.RunbookURL)
	}
	return nil
}

func (h *Handler) updateRule(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	currentSite, ok := h.lookupNullableSiteID(w, r.Context(), func(ctx context.Context) (*uuid.UUID, error) {
		return h.Q.GetAlertRuleSiteScopeID(ctx, id)
	}, "rule not found")
	if !ok {
		return
	}
	if !h.enforceSiteOrGlobal(w, r, currentSite, "alerts:rules:update") {
		return
	}
	var req ruleUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	// If the caller is reassigning the rule to a different site
	// (or to/from enterprise-default), they must own the destination
	// scope too — guards against privilege escalation via reassignment.
	if req.siteSet && !h.enforceSiteOrGlobal(w, r, req.SiteScopeID, "alerts:rules:update") {
		return
	}
	out, err := h.Q.UpdateAlertRule(r.Context(), dbq.UpdateAlertRuleParams{
		ID: id, Name: req.Name,
		DescriptionSet: req.descriptionSet, Description: req.Description,
		Metric: req.Metric, Operator: req.Operator, Threshold: req.Threshold,
		DurationSeconds: req.DurationSeconds, Severity: req.Severity,
		SiteSet: req.siteSet, SiteScopeID: req.SiteScopeID,
		AssetFilterJson: req.AssetFilterJson, Enabled: req.Enabled,
		RunbookSet: req.runbookSet, RunbookURL: req.RunbookURL,
	})
	if err != nil {
		mapErr(w, err, "rule not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "alert_rule.update", TargetType: "alert_rule", TargetID: id.String()})
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteRule(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	currentSite, ok := h.lookupNullableSiteID(w, r.Context(), func(ctx context.Context) (*uuid.UUID, error) {
		return h.Q.GetAlertRuleSiteScopeID(ctx, id)
	}, "rule not found")
	if !ok {
		return
	}
	if !h.enforceSiteOrGlobal(w, r, currentSite, "alerts:rules:delete") {
		return
	}
	if err := h.Q.DeleteAlertRule(r.Context(), id); err != nil {
		mapErr(w, err, "rule not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "alert_rule.delete", TargetType: "alert_rule", TargetID: id.String()})
	w.WriteHeader(http.StatusNoContent)
}

// ---- Maintenance windows ----

type mwCreateReq struct {
	Name            string          `json:"name"`
	SiteID          *uuid.UUID      `json:"site_id"`
	AssetFilterJson json.RawMessage `json:"asset_filter_json"`
	StartsAt        time.Time       `json:"starts_at"`
	EndsAt          time.Time       `json:"ends_at"`
	Reason          *string         `json:"reason"`
}

func (h *Handler) createMW(w http.ResponseWriter, r *http.Request) {
	var req mwCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.Name == "" || req.StartsAt.IsZero() || req.EndsAt.IsZero() {
		httpx.Error(w, http.StatusBadRequest, "name, starts_at, ends_at required")
		return
	}
	// Mirrors Python's _validate_window. Inverted bounds would persist
	// silently and never match the suppression query in
	// services/alerts.py (starts_at <= now AND ends_at >= now).
	if !req.EndsAt.After(req.StartsAt) {
		httpx.Error(w, http.StatusBadRequest, "ends_at must be after starts_at")
		return
	}
	if !h.enforceSiteOrGlobal(w, r, req.SiteID, "maintenance:windows:create") {
		return
	}
	p, _ := auth.From(r.Context())
	var createdBy *string
	if p.Label != "" {
		s := p.Label
		createdBy = &s
	}
	out, err := h.Q.CreateMaintenanceWindow(r.Context(), dbq.CreateMaintenanceWindowParams{
		Name: req.Name, SiteID: req.SiteID, AssetFilterJson: req.AssetFilterJson,
		StartsAt: req.StartsAt, EndsAt: req.EndsAt,
		CreatedBy: createdBy, Reason: req.Reason,
	})
	if err != nil {
		mapErr(w, err, "maintenance window not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "maintenance_window.create", TargetType: "maintenance_window", TargetID: out.ID.String()})
	httpx.JSON(w, http.StatusCreated, out)
}

type mwUpdateReq struct {
	Name            *string
	SiteID          *uuid.UUID
	siteSet         bool
	AssetFilterJson json.RawMessage
	StartsAt        *time.Time
	EndsAt          *time.Time
	Reason          *string
	reasonSet       bool
}

func (u *mwUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["name"]; ok {
		_ = json.Unmarshal(v, &u.Name)
	}
	if v, ok := raw["site_id"]; ok {
		u.siteSet = true
		_ = json.Unmarshal(v, &u.SiteID)
	}
	if v, ok := raw["asset_filter_json"]; ok {
		u.AssetFilterJson = v
	}
	if v, ok := raw["starts_at"]; ok {
		_ = json.Unmarshal(v, &u.StartsAt)
	}
	if v, ok := raw["ends_at"]; ok {
		_ = json.Unmarshal(v, &u.EndsAt)
	}
	if v, ok := raw["reason"]; ok {
		u.reasonSet = true
		_ = json.Unmarshal(v, &u.Reason)
	}
	return nil
}

func (h *Handler) updateMW(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	currentSite, ok := h.lookupNullableSiteID(w, r.Context(), func(ctx context.Context) (*uuid.UUID, error) {
		return h.Q.GetMaintenanceWindowSiteID(ctx, id)
	}, "maintenance window not found")
	if !ok {
		return
	}
	if !h.enforceSiteOrGlobal(w, r, currentSite, "maintenance:windows:update") {
		return
	}
	var req mwUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	if req.siteSet && !h.enforceSiteOrGlobal(w, r, req.SiteID, "maintenance:windows:update") {
		return
	}
	// Merge the patched start/end against the current row so we can
	// validate the post-PATCH window — mirrors Python's
	// _validate_window(new_start, new_end) at the same call site.
	if req.StartsAt != nil || req.EndsAt != nil {
		current, err := h.Q.GetMaintenanceWindow(r.Context(), id)
		if err != nil {
			mapErr(w, err, "maintenance window not found")
			return
		}
		newStart := current.StartsAt
		newEnd := current.EndsAt
		if req.StartsAt != nil {
			newStart = *req.StartsAt
		}
		if req.EndsAt != nil {
			newEnd = *req.EndsAt
		}
		if !newEnd.After(newStart) {
			httpx.Error(w, http.StatusBadRequest, "ends_at must be after starts_at")
			return
		}
	}
	out, err := h.Q.UpdateMaintenanceWindow(r.Context(), dbq.UpdateMaintenanceWindowParams{
		ID: id, Name: req.Name,
		SiteSet: req.siteSet, SiteID: req.SiteID,
		AssetFilterJson: req.AssetFilterJson,
		StartsAt: req.StartsAt, EndsAt: req.EndsAt,
		ReasonSet: req.reasonSet, Reason: req.Reason,
	})
	if err != nil {
		mapErr(w, err, "maintenance window not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "maintenance_window.update", TargetType: "maintenance_window", TargetID: id.String()})
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteMW(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	currentSite, ok := h.lookupNullableSiteID(w, r.Context(), func(ctx context.Context) (*uuid.UUID, error) {
		return h.Q.GetMaintenanceWindowSiteID(ctx, id)
	}, "maintenance window not found")
	if !ok {
		return
	}
	if !h.enforceSiteOrGlobal(w, r, currentSite, "maintenance:windows:delete") {
		return
	}
	if err := h.Q.DeleteMaintenanceWindow(r.Context(), id); err != nil {
		mapErr(w, err, "maintenance window not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "maintenance_window.delete", TargetType: "maintenance_window", TargetID: id.String()})
	w.WriteHeader(http.StatusNoContent)
}
