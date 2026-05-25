// PR 72 — POST /dns/health-checks/{id}/result.
//
// Collector callback after running one probe. Audit is intentionally
// skipped — every 30s probe would flood the audit log; the central
// worker also writes this row on its fallback cycles. last_checked_at
// advances unconditionally so the worker can distinguish "recently
// probed externally" from "stale."
package dns

import (
	"encoding/json"
	"net/http"

	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type healthCheckResultReq struct {
	Status string  `json:"status"`
	Error  *string `json:"error"`
}

// validHealthCheckStatuses mirrors the DnsHealthCheckStatus enum in
// services.dns. Mapping unknown values would let collectors poison
// the column with garbage, so we validate at the boundary.
var validHealthCheckStatuses = map[string]struct{}{
	"unknown":   {},
	"healthy":   {},
	"unhealthy": {},
}

func (h *Handler) postHealthCheckResult(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	var req healthCheckResultReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	if _, ok := validHealthCheckStatuses[req.Status]; !ok {
		httpx.Error(w, http.StatusBadRequest,
			"status must be one of: unknown, healthy, unhealthy")
		return
	}
	// Truncate error text to fit the column. Python schema doesn't
	// enforce a max but the column is varchar(512); collectors
	// occasionally dump stack traces.
	var errPtr *string
	if req.Error != nil {
		e := *req.Error
		if len(e) > 512 {
			e = e[:512]
		}
		errPtr = &e
	}
	n, err := h.Q.SetDnsHealthCheckResult(r.Context(), id, req.Status, errPtr)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if n == 0 {
		httpx.Error(w, http.StatusNotFound, "health check not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
