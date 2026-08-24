// Package driftsummary aggregates DHCP drift state into the fleet /
// per-fabric / per-server triple Python's /dhcp/drift-summary
// endpoint emits (services/dhcp_drift_summary.py:aggregate).
//
// Pure: takes loaded server rows + scope drift rows + firing-alert
// dedupe keys and produces the response shape. The HTTP handler owns
// the SELECTs (with ABAC fabric-scope filter); this package stays
// decoupled from the DB session so the unit suite can exercise the
// roll-up math without standing up Postgres.
package driftsummary

import (
	"strings"
	"time"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// diffStatuses pins the fixed-key tally every level (server, fabric,
// fleet) reports. The shape is stable across runs even when a server
// has zero scopes in a given bucket; dashboards relying on
// counts.drifted never see a missing key. Matches Python's
// _DIFF_STATUSES at services/dhcp_drift_summary.py:24.
var diffStatuses = []string{"in_sync", "drifted", "missing_from_kea", "never_pushed", "error"}

// dhcpDriftAlertPrefix is the per-scope alert dedupe key the PR 87
// dispatcher uses. The aggregator splits on this exact prefix to
// recover the scope UUID — Python's `key.split(":", 1)[1]` semantics
// (the scope UUID can itself contain "-" but never ":", so a 2-way
// split on the first colon is safe).
const dhcpDriftAlertPrefix = "dhcp-drift:"

// ServerSummary mirrors Python's ServerDriftSummary dataclass at
// services/dhcp_drift_summary.py:27. LastPushAt is rendered as a
// string (not *time.Time) so the wire format matches Python's
// isoformat() output for tz-aware UTC datetimes: 6 micros + signed
// offset (e.g. "2024-01-15T12:00:00.000000+00:00"). Go's default
// *time.Time JSON marshal would emit "...Z" instead, breaking
// strict ISO-8601-with-offset consumers.
type ServerSummary struct {
	ServerID       string         `json:"server_id"`
	ServerName     string         `json:"server_name"`
	FabricID       string         `json:"fabric_id"`
	Enabled        bool           `json:"enabled"`
	LastPushAt     *string        `json:"last_push_at"`
	LastPushStatus *string        `json:"last_push_status"`
	ScopesTotal    int            `json:"scopes_total"`
	ScopeCounts    map[string]int `json:"scope_counts"`
	AlertsFiring   int            `json:"alerts_firing"`
}

// pythonISOTZ is the format string Python's `datetime.isoformat()`
// emits for tz-aware UTC datetimes: 6-digit microseconds + signed
// offset. Matches the AttemptedAt format in PR 5
// (internal/ipam/dhcp_push.go) and the timestamps in PR 8
// (dhcp_scope_templates.go) so consumers parse every otter-go
// timestamp with one ISO-8601 mode.
const pythonISOTZ = "2006-01-02T15:04:05.000000-07:00"

// formatLastPushAt renders a nullable timestamp into Python's
// isoformat() shape. Returns nil for nil so the wire emits
// `"last_push_at": null` matching Python's None.
func formatLastPushAt(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(pythonISOTZ)
	return &s
}

// FabricSummary mirrors Python's FabricDriftSummary dataclass
// (PR 102 in Python; per-fabric slice between fleet and per-server).
type FabricSummary struct {
	FabricID         string         `json:"fabric_id"`
	ServersTotal     int            `json:"servers_total"`
	ServersWithDrift int            `json:"servers_with_drift"`
	ScopesTotal      int            `json:"scopes_total"`
	ScopeCounts      map[string]int `json:"scope_counts"`
	AlertsFiring     int            `json:"alerts_firing"`
}

// FleetSummary mirrors Python's FleetDriftSummary dataclass.
type FleetSummary struct {
	ServersTotal     int            `json:"servers_total"`
	ServersWithDrift int            `json:"servers_with_drift"`
	ScopesTotal      int            `json:"scopes_total"`
	ScopeCounts      map[string]int `json:"scope_counts"`
	AlertsFiring     int            `json:"alerts_firing"`
}

// emptyScopeCounts returns a fresh fixed-key map with every status
// at zero. Python's _scope_count_template() equivalent — used so
// every level starts with the full key set even before any scope
// folds in.
func emptyScopeCounts() map[string]int {
	out := make(map[string]int, len(diffStatuses))
	for _, s := range diffStatuses {
		out[s] = 0
	}
	return out
}

// EmptyFleetSummary returns the zero-fleet response shape the
// handler emits when the ABAC scope is empty (Python's first
// short-circuit at api/ipam.py:2632-2645). Pulled out so the
// handler doesn't repeat the literal map.
func EmptyFleetSummary() FleetSummary {
	return FleetSummary{
		ServersTotal: 0, ServersWithDrift: 0,
		ScopesTotal: 0, ScopeCounts: emptyScopeCounts(),
		AlertsFiring: 0,
	}
}

// Aggregate builds the fleet roll-up + per-fabric slice + per-server
// summaries from the three SELECT result sets.
//
// scopesByServer maps server.id → its (possibly empty) drift-status
// rows. alertCountsByScopeID maps scope.id (string form) → the count
// of firing dhcp-drift alerts targeting that scope (Python's
// alert_counts dict from api/ipam.py:2684-2689). The handler builds
// both by indexing the SELECT results before calling Aggregate.
//
// Returns (fleet, fabrics, servers). The fabrics list is in the
// insertion order of fabric_ids — same posture as Python's
// `by_fabric: dict` whose iteration order matches insertion.
func Aggregate(
	servers []dbq.ListDhcpServersForDriftSummaryRow,
	scopesByServer map[uuid.UUID][]dbq.ListDhcpScopeDriftStatusByServersRow,
	alertCountsByScopeID map[string]int,
) (FleetSummary, []FabricSummary, []ServerSummary) {
	fleet := emptyScopeCounts()
	fleetAlerts := 0
	serversWithDrift := 0
	scopesTotal := 0
	summaries := make([]ServerSummary, 0, len(servers))

	// PR 102 — per-fabric accumulators. fabricOrder tracks insertion
	// order so the emitted list iterates in the same sequence as
	// Python's dict iteration (servers come from the SELECT's
	// ORDER BY name, so the fabric order is name-stable).
	byFabric := map[string]*FabricSummary{}
	fabricOrder := []string{}

	for _, srv := range servers {
		scopeRows := scopesByServer[srv.ID]
		counts, serverAlerts := tallyServerScopes(scopeRows, alertCountsByScopeID)
		hasDrift := counts["drifted"] > 0
		if hasDrift {
			serversWithDrift++
		}
		for k, v := range counts {
			fleet[k] += v
		}
		fleetAlerts += serverAlerts
		scopesTotal += len(scopeRows)
		summaries = append(summaries, buildServerSummary(srv, counts, serverAlerts, len(scopeRows)))
		foldIntoFabric(byFabric, &fabricOrder, srv.FabricID.String(), counts, serverAlerts, len(scopeRows), hasDrift)
	}

	fabrics := make([]FabricSummary, len(fabricOrder))
	for i, fid := range fabricOrder {
		fabrics[i] = *byFabric[fid]
	}
	return FleetSummary{
		ServersTotal:     len(servers),
		ServersWithDrift: serversWithDrift,
		ScopesTotal:      scopesTotal,
		ScopeCounts:      fleet,
		AlertsFiring:     fleetAlerts,
	}, fabrics, summaries
}

// tallyServerScopes counts one server's drift status distribution +
// totals its firing-alert count. Pulled out of Aggregate so the main
// loop stays under SonarCloud's cognitive-complexity ceiling. NULL
// last_diff_status maps to "never_pushed"; any status outside the
// fixed-key set lands in "error" (defensive — matches Python at
// services/dhcp_drift_summary.py:109-112).
func tallyServerScopes(scopes []dbq.ListDhcpScopeDriftStatusByServersRow, alertCountsByScopeID map[string]int) (map[string]int, int) {
	counts := emptyScopeCounts()
	alerts := 0
	for _, sc := range scopes {
		status := "never_pushed"
		if sc.LastDiffStatus != nil {
			status = *sc.LastDiffStatus
		}
		if _, ok := counts[status]; !ok {
			status = "error"
		}
		counts[status]++
		alerts += alertCountsByScopeID[sc.ID.String()]
	}
	return counts, alerts
}

// buildServerSummary assembles one ServerSummary value. Extracted
// from Aggregate to keep the main loop body short.
func buildServerSummary(srv dbq.ListDhcpServersForDriftSummaryRow, counts map[string]int, alerts, scopesTotal int) ServerSummary {
	return ServerSummary{
		ServerID:       srv.ID.String(),
		ServerName:     srv.Name,
		FabricID:       srv.FabricID.String(),
		Enabled:        srv.Enabled,
		LastPushAt:     formatLastPushAt(srv.LastPushAt),
		LastPushStatus: srv.LastPushStatus,
		ScopesTotal:    scopesTotal,
		ScopeCounts:    counts,
		AlertsFiring:   alerts,
	}
}

// foldIntoFabric updates the per-fabric accumulator with one server's
// contribution. Creates the FabricSummary on first sight (and
// appends to fabricOrder so the emitted list preserves server-
// iteration order). counts is read-only; the caller mustn't mutate
// it after this call.
func foldIntoFabric(
	byFabric map[string]*FabricSummary,
	fabricOrder *[]string,
	fid string,
	counts map[string]int,
	alerts, scopesTotal int,
	hasDrift bool,
) {
	fab, ok := byFabric[fid]
	if !ok {
		fab = &FabricSummary{
			FabricID: fid, ServersTotal: 0, ServersWithDrift: 0,
			ScopesTotal: 0, ScopeCounts: emptyScopeCounts(),
			AlertsFiring: 0,
		}
		byFabric[fid] = fab
		*fabricOrder = append(*fabricOrder, fid)
	}
	fab.ServersTotal++
	fab.ScopesTotal += scopesTotal
	fab.AlertsFiring += alerts
	for k, v := range counts {
		fab.ScopeCounts[k] += v
	}
	if hasDrift {
		fab.ServersWithDrift++
	}
}

// AlertCountsByScopeID parses a list of dedupe_key strings into the
// per-scope count map the handler hands to Aggregate. The PR 87
// dispatcher writes one row per scope so the count is 0 or 1 per
// scope — we still tally instead of set to 1 in case a future
// dispatcher relaxes the uniqueness constraint.
func AlertCountsByScopeID(dedupeKeys []string) map[string]int {
	out := map[string]int{}
	for _, k := range dedupeKeys {
		if !strings.HasPrefix(k, dhcpDriftAlertPrefix) {
			continue
		}
		scopeID := strings.TrimPrefix(k, dhcpDriftAlertPrefix)
		if scopeID == "" {
			continue
		}
		out[scopeID]++
	}
	return out
}

// ScopesByServer indexes the flat scope result by server id.
func ScopesByServer(rows []dbq.ListDhcpScopeDriftStatusByServersRow) map[uuid.UUID][]dbq.ListDhcpScopeDriftStatusByServersRow {
	out := map[uuid.UUID][]dbq.ListDhcpScopeDriftStatusByServersRow{}
	for _, r := range rows {
		out[r.DhcpServerID] = append(out[r.DhcpServerID], r)
	}
	return out
}
