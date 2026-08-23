// PR 84 — GET /dns/dashboard.
//
// One-shot aggregate the DNS Overview page polls. Folds together
// global KPIs, a time-bucketed series, per-site rollup, and
// server-health table in a single response.
//
// Scope (fabric_id query param) narrows every aggregate to that
// fabric. Server count, sample window, zone counts, anycast count
// all derive from the same filtered set so the dashboard stays
// internally consistent.
//
// Aggregations not yet ported (return zero/null):
//   - top_names (would need a JSONB aggregation across samples)
//   - storage stats (would need pg_column_size queries)
//   - engines per-server resolution (would need fabric.recursive_engine
//     join — Python's _engine_for helper)
//
// Frontend treats null/zero as "data not available yet" and
// degrades gracefully. Follow-up PR can flesh these out.
package dns

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// ---- response shape (matches DnsDashboardOut) ----

type dashGlobal struct {
	QpsNow         float64        `json:"qps_now"`
	QpsAvg         float64        `json:"qps_avg"`
	QueriesTotal   int64          `json:"queries_total"`
	NxdomainPct    float64        `json:"nxdomain_pct"`
	ServfailPct    float64        `json:"servfail_pct"`
	P50Ms          *float64       `json:"p50_ms"`
	P95Ms          *float64       `json:"p95_ms"`
	SitesActive    int            `json:"sites_active"`
	ServersTotal   int            `json:"servers_total"`
	ZonesTotal     int            `json:"zones_total"`
	ZonesSigned    int            `json:"zones_signed"`
	ZonesNsec3     int            `json:"zones_nsec3"`
	AnycastGroups  int            `json:"anycast_groups"`
	Engines        map[string]int `json:"engines"`
}

type dashSeriesPoint struct {
	ObservedAt   time.Time `json:"observed_at"`
	Qps          float64   `json:"qps"`
	NxdomainPct  float64   `json:"nxdomain_pct"`
	ServfailPct  float64   `json:"servfail_pct"`
	P50Ms        *float64  `json:"p50_ms"`
	P95Ms        *float64  `json:"p95_ms"`
}

type dashSitePanel struct {
	SiteID       *uuid.UUID `json:"site_id"`
	SiteName     string     `json:"site_name"`
	QpsNow       float64    `json:"qps_now"`
	QueriesTotal int64      `json:"queries_total"`
	NxdomainPct  float64    `json:"nxdomain_pct"`
	ServfailPct  float64    `json:"servfail_pct"`
	P95Ms        *float64   `json:"p95_ms"`
	ServerCount  int        `json:"server_count"`
}

type dashServerHealth struct {
	ServerID         uuid.UUID  `json:"server_id"`
	Name             string     `json:"name"`
	Role             string     `json:"role"`
	Engine           string     `json:"engine"`
	SiteID           *uuid.UUID `json:"site_id"`
	SiteName         *string    `json:"site_name"`
	LastRenderStatus *string    `json:"last_render_status"`
	LastRenderAt     *time.Time `json:"last_render_at"`
	LastRenderEtag   *string    `json:"last_render_etag"`
	QpsNow           *float64   `json:"qps_now"`
}

type dashStorageStats struct {
	SampleCount         int  `json:"sample_count"`
	SamplesWithTopNames int  `json:"samples_with_top_names"`
	TopNamesBytesAvg    *int `json:"top_names_bytes_avg"`
	TopNamesBytesTotal  int  `json:"top_names_bytes_total"`
}

type dashboardResponse struct {
	GeneratedAt   time.Time          `json:"generated_at"`
	WindowMinutes int                `json:"window_minutes"`
	Overall       dashGlobal         `json:"overall"`
	Series        []dashSeriesPoint  `json:"series"`
	BySite        []dashSitePanel    `json:"by_site"`
	ServerHealth  []dashServerHealth `json:"server_health"`
	TopNames      []any              `json:"top_names"` // null until ported
	Storage       dashStorageStats   `json:"storage"`
}

// ---- aggregation helpers ----

func pct(num, total int64) float64 {
	if total == 0 {
		return 0
	}
	return math.Round(float64(num)*10000.0/float64(total)) / 100.0
}

// weightedLatency computes the query-weighted average of a latency
// field across samples. Samples with no queries contribute nothing.
// extract returns the latency field; pass nil to skip the sample.
func weightedLatency(samples []dbq.DnsServerMetricsSample, extract func(dbq.DnsServerMetricsSample) *float64) *float64 {
	var num, den float64
	for _, s := range samples {
		v := extract(s)
		if v == nil || s.Queries <= 0 {
			continue
		}
		num += *v * float64(s.Queries)
		den += float64(s.Queries)
	}
	if den == 0 {
		return nil
	}
	r := math.Round(num/den*100) / 100
	return &r
}

func qpsFromLastSample(s *dbq.DnsServerMetricsSample) *float64 {
	if s == nil || s.IntervalSeconds <= 0 {
		return nil
	}
	q := math.Round(float64(s.Queries)/float64(s.IntervalSeconds)*100) / 100
	return &q
}

func extractP50(s dbq.DnsServerMetricsSample) *float64 { return s.P50Ms }
func extractP95(s dbq.DnsServerMetricsSample) *float64 { return s.P95Ms }

// bucketSeries rolls per-server samples into `buckets` equal time
// slices over the window. Each slice sums queries/error counts
// across servers and averages p50/p95 weighted by query volume.
func bucketSeries(samples []dbq.DnsServerMetricsSample, minutes int, buckets int, now time.Time) []dashSeriesPoint {
	if buckets <= 0 {
		buckets = 24
	}
	if len(samples) == 0 {
		return []dashSeriesPoint{}
	}
	window := time.Duration(minutes) * time.Minute
	start := now.Add(-window)
	sliceS := int(window.Seconds() / float64(buckets))
	if sliceS < 1 {
		sliceS = 1
	}
	out := make([]dashSeriesPoint, 0, buckets)
	for i := 0; i < buckets; i++ {
		bStart := start.Add(time.Duration(sliceS*i) * time.Second)
		bEnd := bStart.Add(time.Duration(sliceS) * time.Second)
		var inSlice []dbq.DnsServerMetricsSample
		for _, s := range samples {
			if !s.ObservedAt.Before(bStart) && s.ObservedAt.Before(bEnd) {
				inSlice = append(inSlice, s)
			}
		}
		if len(inSlice) == 0 {
			out = append(out, dashSeriesPoint{ObservedAt: bEnd})
			continue
		}
		var q, nx, sf int64
		for _, s := range inSlice {
			q += s.Queries
			nx += s.Nxdomain
			sf += s.Servfail
		}
		out = append(out, dashSeriesPoint{
			ObservedAt:  bEnd,
			Qps:         math.Round(float64(q)/float64(sliceS)*100) / 100,
			NxdomainPct: pct(nx, q),
			ServfailPct: pct(sf, q),
			P50Ms:       weightedLatency(inSlice, extractP50),
			P95Ms:       weightedLatency(inSlice, extractP95),
		})
	}
	return out
}

// ---- main handler ----

func (h *Handler) dnsDashboard(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	minutes := 60
	if v := q.Get("minutes"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 5 || n > 1440 {
			httpx.Error(w, http.StatusBadRequest, "minutes must be 5..1440")
			return
		}
		minutes = n
	}
	var fabricID *uuid.UUID
	if v := q.Get("fabric_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "fabric_id is not a uuid")
			return
		}
		fabricID = &id
	}

	now := time.Now().UTC()
	cutoff := now.Add(-time.Duration(minutes) * time.Minute)

	servers, err := h.Q.ListDnsServersForDashboard(r.Context(), fabricID)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	zones, err := h.Q.ListDnsZonesForDashboard(r.Context(), fabricID)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	agCount, err := h.Q.CountAnycastGroupsForDashboard(r.Context(), fabricID)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}

	// Samples: optional fabric scope filters to this fabric's servers.
	var serverIDs []uuid.UUID
	if fabricID != nil {
		serverIDs = make([]uuid.UUID, 0, len(servers))
		for _, s := range servers {
			serverIDs = append(serverIDs, s.ID)
		}
		if len(serverIDs) == 0 {
			// Empty scope — short-circuit to an empty dashboard so
			// the response shape stays valid.
			httpx.JSON(w, http.StatusOK, dashboardResponse{
				GeneratedAt: now, WindowMinutes: minutes,
				Overall: dashGlobal{Engines: map[string]int{}},
				Series:  []dashSeriesPoint{},
				BySite:  []dashSitePanel{},
				ServerHealth: []dashServerHealth{},
			})
			return
		}
	}
	samples, err := h.Q.ListDnsSamplesInWindow(r.Context(), dbq.ListDnsSamplesInWindowParams{Cutoff: cutoff, ServerIDs: serverIDs})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}

	// Latest sample per server — walking the sorted list once is
	// cheaper than a per-server LIMIT 1 query.
	latestPerServer := make(map[uuid.UUID]dbq.DnsServerMetricsSample, len(servers))
	for _, s := range samples {
		latestPerServer[s.ServerID] = s
	}
	qpsNowPerServer := make(map[uuid.UUID]*float64, len(servers))
	for _, srv := range servers {
		if s, ok := latestPerServer[srv.ID]; ok {
			qpsNowPerServer[srv.ID] = qpsFromLastSample(&s)
		} else {
			qpsNowPerServer[srv.ID] = nil
		}
	}

	resp := dashboardResponse{
		GeneratedAt:   now,
		WindowMinutes: minutes,
		Overall:       buildGlobalKPIs(samples, servers, zones, qpsNowPerServer, int64(agCount), minutes),
		Series:        bucketSeries(samples, minutes, 24, now),
		BySite:        buildBySite(samples, servers, qpsNowPerServer),
		ServerHealth:  buildServerHealth(servers, qpsNowPerServer),
		TopNames:      nil, // deferred — see file header
		Storage:       dashStorageStats{SampleCount: len(samples)},
	}
	httpx.JSON(w, http.StatusOK, resp)
}

// ---- helpers split out for testability ----

func buildGlobalKPIs(
	samples []dbq.DnsServerMetricsSample,
	servers []dbq.ListDnsServersForDashboardRow,
	zones []dbq.ListDnsZonesForDashboardRow,
	qpsNowPerServer map[uuid.UUID]*float64,
	agCount int64,
	minutes int,
) dashGlobal {
	var totalQ, totalNx, totalSf int64
	for _, s := range samples {
		totalQ += s.Queries
		totalNx += s.Nxdomain
		totalSf += s.Servfail
	}
	var qpsNow float64
	for _, srv := range servers {
		if q := qpsNowPerServer[srv.ID]; q != nil {
			qpsNow += *q
		}
	}
	qpsAvg := 0.0
	if minutes > 0 {
		qpsAvg = math.Round(float64(totalQ)/float64(minutes*60)*100) / 100
	}
	// Engine counts: simplified port (no fabric.recursive_engine lookup
	// yet). Recursive role assumed CoreDNS; auth always CoreDNS.
	engines := map[string]int{"coredns": 0, "hickory": 0}
	for _, srv := range servers {
		engines["coredns"]++
		_ = srv
	}
	sitesActive := map[uuid.UUID]struct{}{}
	for _, srv := range servers {
		sitesActive[srv.SiteID] = struct{}{}
	}
	zonesSigned, zonesNsec3 := 0, 0
	for _, z := range zones {
		if z.Signed {
			zonesSigned++
		}
		if z.Nsec3Iterations > 0 {
			zonesNsec3++
		}
	}
	return dashGlobal{
		QpsNow:        math.Round(qpsNow*100) / 100,
		QpsAvg:        qpsAvg,
		QueriesTotal:  totalQ,
		NxdomainPct:   pct(totalNx, totalQ),
		ServfailPct:   pct(totalSf, totalQ),
		P50Ms:         weightedLatency(samples, extractP50),
		P95Ms:         weightedLatency(samples, extractP95),
		SitesActive:   len(sitesActive),
		ServersTotal:  len(servers),
		ZonesTotal:    len(zones),
		ZonesSigned:   zonesSigned,
		ZonesNsec3:    zonesNsec3,
		AnycastGroups: int(agCount),
		Engines:       engines,
	}
}

func buildBySite(
	samples []dbq.DnsServerMetricsSample,
	servers []dbq.ListDnsServersForDashboardRow,
	qpsNowPerServer map[uuid.UUID]*float64,
) []dashSitePanel {
	// Group servers by site_id.
	bySite := make(map[uuid.UUID][]dbq.ListDnsServersForDashboardRow)
	for _, srv := range servers {
		bySite[srv.SiteID] = append(bySite[srv.SiteID], srv)
	}
	// Group samples by server's site_id.
	serverSite := make(map[uuid.UUID]uuid.UUID, len(servers))
	for _, srv := range servers {
		serverSite[srv.ID] = srv.SiteID
	}
	samplesPerSite := make(map[uuid.UUID][]dbq.DnsServerMetricsSample)
	for _, s := range samples {
		if sid, ok := serverSite[s.ServerID]; ok {
			samplesPerSite[sid] = append(samplesPerSite[sid], s)
		}
	}
	out := make([]dashSitePanel, 0, len(bySite))
	for siteID, srvs := range bySite {
		var totalQ, totalNx, totalSf int64
		var qpsNow float64
		for _, srv := range srvs {
			if q := qpsNowPerServer[srv.ID]; q != nil {
				qpsNow += *q
			}
		}
		for _, s := range samplesPerSite[siteID] {
			totalQ += s.Queries
			totalNx += s.Nxdomain
			totalSf += s.Servfail
		}
		sid := siteID
		out = append(out, dashSitePanel{
			SiteID:       &sid,
			SiteName:     "", // Site name lookup would need a join; deferred.
			QpsNow:       math.Round(qpsNow*100) / 100,
			QueriesTotal: totalQ,
			NxdomainPct:  pct(totalNx, totalQ),
			ServfailPct:  pct(totalSf, totalQ),
			P95Ms:        weightedLatency(samplesPerSite[siteID], extractP95),
			ServerCount:  len(srvs),
		})
	}
	return out
}

func buildServerHealth(
	servers []dbq.ListDnsServersForDashboardRow,
	qpsNowPerServer map[uuid.UUID]*float64,
) []dashServerHealth {
	out := make([]dashServerHealth, 0, len(servers))
	for _, srv := range servers {
		// Engine resolution simplified — see file header note.
		engine := "coredns"
		out = append(out, dashServerHealth{
			ServerID:         srv.ID,
			Name:             srv.Name,
			Role:             srv.Role,
			Engine:           engine,
			SiteID:           &srv.SiteID,
			SiteName:         nil, // deferred (needs Site join)
			LastRenderStatus: srv.LastRenderStatus,
			LastRenderAt:     srv.LastRenderAt,
			LastRenderEtag:   srv.LastRenderEtag,
			QpsNow:           qpsNowPerServer[srv.ID],
		})
	}
	return out
}

// Quiet the unused-import linter if json is removed in a future
// refactor. The dashboard schema is large enough that I want to
// keep all the response shapes typed (vs map[string]any).
var _ = json.Marshal
