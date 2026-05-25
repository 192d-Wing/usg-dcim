// PR 73 — POST /dns/servers/{id}/render-status.
//
// Collector callback after every render attempt. Mirrors the
// DhcpServer last_sync_* shape on DnsServer: last_render_at
// advances unconditionally; status / error / etag overwrite;
// coredns_version overwrites only when the collector knows it
// (COALESCE in SQL preserves the prior value on NULL).
//
// Audit is intentionally skipped — render attempts are high-
// frequency (every config change) and would flood the audit
// log. The collector identity in the operator-visible "last
// rendered by" is implicit in the etag.
package dns

import (
	"encoding/json"
	"net/http"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type renderStatusReq struct {
	Status         string  `json:"status"`
	Error          *string `json:"error"`
	Etag           *string `json:"etag"`
	CoreDNSVersion *string `json:"coredns_version"`
}

// validRenderStatuses caps the column to known values. Renderer
// status came from a finite set in services.dns; "unknown" lets
// the collector flag a half-completed attempt without inventing a
// new status.
var validRenderStatuses = map[string]struct{}{
	"unknown": {},
	"ok":      {},
	"error":   {},
}

type renderStatusResp struct {
	ServerID string `json:"server_id"`
	Status   string `json:"status"`
}

func (h *Handler) postServerRenderStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	var req renderStatusReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	if _, ok := validRenderStatuses[req.Status]; !ok {
		httpx.Error(w, http.StatusBadRequest,
			"status must be one of: unknown, ok, error")
		return
	}
	n, err := h.Q.SetDnsServerRenderStatus(r.Context(), dbq.SetDnsServerRenderStatusParams{
		ID: id, Status: req.Status, Error: req.Error,
		Etag: req.Etag, CoreDNSVersion: req.CoreDNSVersion,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if n == 0 {
		httpx.Error(w, http.StatusNotFound, "server not found")
		return
	}
	httpx.JSON(w, http.StatusOK, renderStatusResp{
		ServerID: id.String(), Status: req.Status,
	})
}
