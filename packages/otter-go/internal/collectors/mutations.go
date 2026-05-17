// Collector mutations (PR 44). Enroll + heartbeat are deferred —
// they need crypto (mTLS fingerprint, enrollment token hashing) and
// audit wiring that lives on the Python side until the audit module
// port lands.
package collectors

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
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
		httpx.Error(w, http.StatusBadRequest, "bad request body")
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
		httpx.Error(w, http.StatusBadRequest, "bad request body")
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
