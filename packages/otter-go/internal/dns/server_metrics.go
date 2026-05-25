// PR 78 — /dns/servers/{id}/metrics POST + GET.
//
// Per-interval delta samples from the dns-collector's CoreDNS
// Prometheus scrape. Audit is skipped on both paths: POSTs are
// high-volume cron telemetry (one row per server every 30s) and
// GETs are read-only.
//
// The retention sweep lives in services.dns on the Python side
// and isn't ported — it runs as an arq cron job, not as an HTTP
// endpoint, so it stays out of this surface.
package dns

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// metricsSampleReq mirrors DnsMetricsSampleIn. observed_at is
// optional — when missing, SQL's COALESCE defaults to NOW().
type metricsSampleReq struct {
	ObservedAt      *time.Time             `json:"observed_at"`
	IntervalSeconds int32                  `json:"interval_seconds"`
	Queries         int64                  `json:"queries"`
	Nxdomain        int64                  `json:"nxdomain"`
	Servfail        int64                  `json:"servfail"`
	Noerror         int64                  `json:"noerror"`
	P50Ms           *float64               `json:"p50_ms"`
	P95Ms           *float64               `json:"p95_ms"`
	TopNames        []metricsTopNameEntry  `json:"top_names"`
	TopNamesSet     bool                   `json:"-"`
}

type metricsTopNameEntry struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Count int64  `json:"count"`
}

// UnmarshalJSON tracks whether top_names was set on the wire so
// nil and missing can be distinguished. Mirrors Python's behavior:
// nil = "collector hasn't wired dnstap on this server", empty
// list = "wired but observed zero queries in the window".
func (r *metricsSampleReq) UnmarshalJSON(data []byte) error {
	type alias metricsSampleReq
	var raw struct {
		alias
		TopNames json.RawMessage `json:"top_names"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*r = metricsSampleReq(raw.alias)
	if raw.TopNames != nil {
		r.TopNamesSet = true
		// Distinguish null from a non-null array: json.RawMessage
		// preserves "null" as bytes, so check explicitly.
		if string(raw.TopNames) != "null" {
			if err := json.Unmarshal(raw.TopNames, &r.TopNames); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *Handler) postServerMetrics(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	var req metricsSampleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	if req.IntervalSeconds < 1 {
		httpx.Error(w, http.StatusBadRequest, "interval_seconds must be >= 1")
		return
	}
	if req.Queries < 0 || req.Nxdomain < 0 || req.Servfail < 0 || req.Noerror < 0 {
		httpx.Error(w, http.StatusBadRequest, "counters must be non-negative")
		return
	}
	// Verify the parent server exists with a 404 distinct from
	// "FK violation" so collectors can tell "server deleted" from
	// "transient DB error."
	if _, err := h.Q.GetDnsServer(r.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "server not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	// Encode top_names: nil stays NULL in DB; empty list serializes
	// as "[]" (JSONB null vs empty array — both valid).
	var topNamesJSON json.RawMessage
	if req.TopNamesSet {
		// Even if TopNames is nil at this point (empty array case
		// would also reach here), serialize cleanly.
		b, err := json.Marshal(req.TopNames)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "top_names must be a JSON array")
			return
		}
		topNamesJSON = b
	}
	out, err := h.Q.CreateDnsServerMetricsSample(r.Context(), dbq.CreateDnsServerMetricsSampleParams{
		ServerID:        id,
		ObservedAt:      req.ObservedAt,
		IntervalSeconds: req.IntervalSeconds,
		Queries:         req.Queries,
		Nxdomain:        req.Nxdomain,
		Servfail:        req.Servfail,
		Noerror:         req.Noerror,
		P50Ms:           req.P50Ms,
		P95Ms:           req.P95Ms,
		TopNames:        topNamesJSON,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *Handler) listServerMetrics(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	// minutes: default 60, range 1..1440 (24h). Matches Python.
	minutes := 60
	if v := r.URL.Query().Get("minutes"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1440 {
			httpx.Error(w, http.StatusBadRequest, "minutes must be 1..1440")
			return
		}
		minutes = n
	}
	if _, err := h.Q.GetDnsServer(r.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "server not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	cutoff := time.Now().UTC().Add(-time.Duration(minutes) * time.Minute)
	rows, err := h.Q.ListDnsServerMetricsSamples(r.Context(), id, cutoff)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, rows)
}

// Compile-time hint: idFromURL is shared with the rest of the
// package via mutations.go.
var _ = uuid.Nil
