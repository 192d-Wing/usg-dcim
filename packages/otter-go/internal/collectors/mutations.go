// Collector mutations. enroll/heartbeat were added in PR 21 alongside
// the cutover; the older config/enabled/decommission handlers were
// already on Go (PR 44).
package collectors

import (
	"crypto/rand"
	"encoding/base64"
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

const msgBadBody = "bad request body"

func idFromURL(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return uuid.Nil, false
	}
	return id, true
}

func mapErr(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "collector not found")
		return
	}
	status, msg := httpx.Mapped(err)
	httpx.Error(w, status, msg)
}

// patchConfig: full-replace of config_overrides. Python's PATCH
// model_dump w/ all-null fields produces {} effectively, so the wire
// shape is "post the whole overrides dict and we store it verbatim".
func (h *Handler) patchConfig(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		httpx.Error(w, http.StatusBadRequest, msgBadBody)
		return
	}
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	out, err := h.Q.SetCollectorConfigOverrides(r.Context(), dbq.SetCollectorConfigOverridesParams{
		ID: id, ConfigOverrides: raw,
	})
	if err != nil {
		mapErr(w, err)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "collector.config_overrides.update", TargetType: "collector", TargetID: id.String()})
	httpx.JSON(w, http.StatusOK, out)
}

type enabledReq struct {
	Enabled bool `json:"enabled"`
}

func (h *Handler) patchEnabled(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	var req enabledReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, msgBadBody)
		return
	}
	out, err := h.Q.SetCollectorEnabled(r.Context(), dbq.SetCollectorEnabledParams{
		ID: id, Enabled: req.Enabled,
	})
	if err != nil {
		mapErr(w, err)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "collector.enabled.update", TargetType: "collector", TargetID: id.String()})
	httpx.JSON(w, http.StatusOK, out)
}

// ===== Enroll =====

type enrollReq struct {
	SiteID       uuid.UUID `json:"site_id"`
	Name         string    `json:"name"`
	Capabilities []string  `json:"capabilities"`
}

type enrollOut struct {
	CollectorID      string `json:"collector_id"`
	EnrollmentToken  string `json:"enrollment_token"`
	ExpiresInSeconds int    `json:"expires_in_seconds"`
}

// enrollTokenExpirySeconds matches Python's hard-coded 3600 in
// api/collectors.py::enroll_collector. The collector must exchange
// the plaintext for an mTLS cert + API token before this window
// elapses (no DB-side TTL on enrollment_token_hash today; enforced
// at the exchange step).
const enrollTokenExpirySeconds = 3600

// generateEnrollmentToken mirrors Python's
// `"enroll_" + secrets.token_urlsafe(32)`. RawURLEncoding strips
// the padding the same way urlsafe_b64encode + rstrip does in
// Python's secrets module.
func generateEnrollmentToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "enroll_" + base64.RawURLEncoding.EncodeToString(buf), nil
}

func (h *Handler) enroll(w http.ResponseWriter, r *http.Request) {
	var req enrollReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, msgBadBody)
		return
	}
	// site_id required — Python's CollectorEnroll types it as UUID
	// (pydantic 422s on missing). name has no Python-side minlength so
	// we accept "" to preserve parity even though it's a bad UX.
	if req.SiteID == uuid.Nil {
		httpx.Error(w, http.StatusBadRequest, "site_id required")
		return
	}
	p, ok := auth.From(r.Context())
	if !ok {
		httpx.Error(w, http.StatusInternalServerError, "missing principal")
		return
	}
	if err := auth.EnforceSiteScope(r.Context(), h.Q, p, req.SiteID, capCollectorsEnroll); err != nil {
		httpx.Error(w, http.StatusForbidden, err.Error())
		return
	}
	caps := req.Capabilities
	if caps == nil {
		caps = []string{}
	}
	capsJSON, err := json.Marshal(caps)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "capabilities must be a JSON array of strings")
		return
	}
	raw, err := generateEnrollmentToken()
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not generate token")
		return
	}
	tokenHash := auth.HashAPIToken(raw)
	row, err := h.Q.EnrollCollector(r.Context(), dbq.EnrollCollectorParams{
		SiteID: req.SiteID, Name: req.Name,
		CapabilitiesJson:    capsJSON,
		EnrollmentTokenHash: tokenHash,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	siteID := row.SiteID
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action:     "collector.enroll",
		TargetType: "collector",
		TargetID:   row.ID.String(),
		SiteID:     &siteID,
	})
	httpx.JSON(w, http.StatusOK, enrollOut{
		CollectorID:      row.ID.String(),
		EnrollmentToken:  raw,
		ExpiresInSeconds: enrollTokenExpirySeconds,
	})
}

// ===== Heartbeat =====

type heartbeatReq struct {
	QueueDepth      int32          `json:"queue_depth"`
	BufferedSamples int32          `json:"buffered_samples"`
	Version         *string        `json:"version"`
	LastError       *string        `json:"last_error"`
	Metrics         map[string]any `json:"metrics"`
}

// configOverrides matches Python's CollectorConfigOverrides. Three
// nullable fields the agent reads to override its YAML ticker
// intervals; a nil value keeps the YAML default.
type configOverrides struct {
	DNSMetricsIntervalSeconds *int `json:"dns_metrics_interval_seconds"`
	DevicePollIntervalSeconds *int `json:"device_poll_interval_seconds"`
	HeartbeatIntervalSeconds  *int `json:"heartbeat_interval_seconds"`
}

type heartbeatOut struct {
	OK              bool            `json:"ok"`
	ReceivedAt      time.Time       `json:"received_at"`
	ConfigOverrides configOverrides `json:"config_overrides"`
}

func (h *Handler) heartbeat(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	var req heartbeatReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, msgBadBody)
		return
	}
	// 404-vs-update parity: Python pre-flights with db.get(Collector, id)
	// and raises NotFoundError if missing. The UPDATE alone returns
	// ErrNoRows from QueryRow.Scan when no row matched, which we map to
	// the same 404 — but pre-checking avoids the misleading "udpate-but-
	// no-match" semantic and lets us mirror Python's flow.
	if _, err := h.Q.GetCollector(r.Context(), id); err != nil {
		mapErr(w, err)
		return
	}
	now := time.Now().UTC()
	status := "healthy"
	if req.LastError != nil && *req.LastError != "" {
		status = "degraded"
	}
	// Empty-string version is treated as "not reported" — matches
	// Python's `if payload.version: coll.version = payload.version`
	// (truthy check). Passing nil into HeartbeatCollector triggers
	// COALESCE → preserves the previously-known version.
	version := req.Version
	if version != nil && *version == "" {
		version = nil
	}
	overridesJSON, err := h.Q.HeartbeatCollector(r.Context(), dbq.HeartbeatCollectorParams{
		ID: id, LastSeenAt: now,
		Version: version, Status: status, BufferedSamples: req.BufferedSamples,
	})
	if err != nil {
		mapErr(w, err)
		return
	}
	metrics := req.Metrics
	if metrics == nil {
		metrics = map[string]any{}
	}
	metricsJSON, err := json.Marshal(metrics)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "metrics must be a JSON object")
		return
	}
	if err := h.Q.InsertCollectorHeartbeat(r.Context(), dbq.InsertCollectorHeartbeatParams{
		CollectorID: id, ReceivedAt: now,
		QueueDepth: req.QueueDepth, LastError: req.LastError,
		MetricsJson: metricsJSON,
	}); err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	var overrides configOverrides
	if len(overridesJSON) > 0 {
		_ = json.Unmarshal(overridesJSON, &overrides)
	}
	httpx.JSON(w, http.StatusOK, heartbeatOut{OK: true, ReceivedAt: now, ConfigOverrides: overrides})
}

// decommission soft-deletes the collector by setting status to
// 'decommissioned'. The row stays so historical reports still resolve;
// flip to 'unreachable' later if you want it fully archived.
func (h *Handler) decommission(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	out, err := h.Q.DecommissionCollector(r.Context(), id)
	if err != nil {
		mapErr(w, err)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{Action: "collector.decommission", TargetType: "collector", TargetID: id.String()})
	httpx.JSON(w, http.StatusOK, out)
}
