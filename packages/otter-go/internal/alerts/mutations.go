// Alerts mutations (PR 45). ack action, rules CRUD, maintenance
// windows CRUD. Resolve + comment endpoints don't exist in Python's
// alerts router (alerts auto-resolve when the underlying condition
// clears) so they aren't ported.
package alerts

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

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
	var req ruleUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
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
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteRule(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if err := h.Q.DeleteAlertRule(r.Context(), id); err != nil {
		mapErr(w, err, "rule not found")
		return
	}
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
	var req mwUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
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
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteMW(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if err := h.Q.DeleteMaintenanceWindow(r.Context(), id); err != nil {
		mapErr(w, err, "maintenance window not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
