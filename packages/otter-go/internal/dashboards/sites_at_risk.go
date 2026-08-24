package dashboards

import (
	"context"
	"net/http"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// validSeverities mirrors the alert_severity Postgres ENUM defined in
// the alembic schema (20260507_0002): info, warning, minor, major,
// critical. Used to reject `?severity=` values the SQL would 22P02 on.
var validSeverities = map[string]struct{}{
	"info":     {},
	"warning":  {},
	"minor":    {},
	"major":    {},
	"critical": {},
}

// SitesAtRiskQuerier is the dbq slice /sites/at-risk needs. Distinct
// from the enterprise + free-space Queriers so each test file's stubs
// stay narrow.
type SitesAtRiskQuerier interface {
	ListSitesAtRisk(ctx context.Context, minSeverity string) ([]dbq.ListSitesAtRiskRow, error)
}

// sitesAtRiskResponse mirrors Python's `{"sites": [{site_id, alert_count}]}`
// byte-for-byte. site_id is rendered as a string to match Python's
// `str(r.site_id)` output.
type sitesAtRiskResponse struct {
	Sites []sitesAtRiskRow `json:"sites"`
}

type sitesAtRiskRow struct {
	SiteID     string `json:"site_id"`
	AlertCount int64  `json:"alert_count"`
}

func (h *Handler) sitesAtRisk(w http.ResponseWriter, r *http.Request) {
	q, ok := h.Q.(SitesAtRiskQuerier)
	if !ok {
		// Defense-in-depth: main.go wires *dbq.Queries which satisfies
		// this; a unit test with a narrower fake would land here.
		httpx.Error(w, http.StatusInternalServerError, "sites-at-risk requires full Querier")
		return
	}

	// Default mirrors Python's `Severity.major`. Invalid values 400 —
	// FastAPI's Pydantic enum coercion would return 422; staying with
	// 400 here matches httpx.Error's lane without surfacing a new
	// status code.
	severity := r.URL.Query().Get("severity")
	if severity == "" {
		severity = "major"
	}
	if _, ok := validSeverities[severity]; !ok {
		httpx.Error(w, http.StatusBadRequest, "severity must be one of info, warning, minor, major, critical")
		return
	}

	rows, err := q.ListSitesAtRisk(r.Context(), severity)
	if err != nil {
		mapErr(w, err)
		return
	}

	out := make([]sitesAtRiskRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, sitesAtRiskRow{
			SiteID:     row.SiteID.String(),
			AlertCount: row.AlertCount,
		})
	}
	httpx.JSON(w, http.StatusOK, sitesAtRiskResponse{Sites: out})
}
