// Unit tests for the pure DHCP drift aggregator. The HTTP wrapper
// is tested separately in internal/ipam; these cover the roll-up
// math, the never_pushed bucketing, the per-fabric slice, and the
// alert-key parsing.
package driftsummary

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

func ptr(s string) *string { return &s }

func TestEmptyFleetSummary_FullStatusKeys(t *testing.T) {
	fleet := EmptyFleetSummary()
	if fleet.ServersTotal != 0 || fleet.AlertsFiring != 0 {
		t.Errorf("non-zero scalars: %+v", fleet)
	}
	for _, key := range diffStatuses {
		if _, ok := fleet.ScopeCounts[key]; !ok {
			t.Errorf("ScopeCounts missing %q", key)
		}
	}
}

func TestAggregate_EmptyServers_ReturnsZeroFleet(t *testing.T) {
	fleet, fabrics, servers := Aggregate(nil, nil, nil)
	if fleet.ServersTotal != 0 || fleet.ScopesTotal != 0 {
		t.Errorf("fleet: %+v", fleet)
	}
	if len(fabrics) != 0 || len(servers) != 0 {
		t.Errorf("expected empty slices, got fabrics=%d servers=%d", len(fabrics), len(servers))
	}
	// Even on empty input the fleet status map carries every key
	// at zero so dashboards don't see missing fields.
	for _, key := range diffStatuses {
		if fleet.ScopeCounts[key] != 0 {
			t.Errorf("ScopeCounts[%q] = %d, want 0", key, fleet.ScopeCounts[key])
		}
	}
}

func TestAggregate_NeverPushedFromNullStatus(t *testing.T) {
	srvID := uuid.New()
	srv := dbq.DhcpServerDriftSummaryRow{ID: srvID, Name: "kea-1", FabricID: uuid.New(), Enabled: true}
	scopes := map[uuid.UUID][]dbq.DhcpScopeDriftStatusRow{
		srvID: {
			{ID: uuid.New(), DhcpServerID: srvID, LastDiffStatus: nil},
			{ID: uuid.New(), DhcpServerID: srvID, LastDiffStatus: nil},
		},
	}
	fleet, fabrics, summaries := Aggregate([]dbq.DhcpServerDriftSummaryRow{srv}, scopes, nil)
	if summaries[0].ScopeCounts["never_pushed"] != 2 {
		t.Errorf("never_pushed = %d, want 2 (NULL last_diff_status maps to never_pushed)", summaries[0].ScopeCounts["never_pushed"])
	}
	if fleet.ScopeCounts["never_pushed"] != 2 {
		t.Errorf("fleet never_pushed = %d, want 2", fleet.ScopeCounts["never_pushed"])
	}
	if fabrics[0].ScopeCounts["never_pushed"] != 2 {
		t.Errorf("fabric never_pushed = %d, want 2", fabrics[0].ScopeCounts["never_pushed"])
	}
	if fleet.ServersWithDrift != 0 {
		t.Errorf("never_pushed scopes shouldn't count as drifted, got %d", fleet.ServersWithDrift)
	}
}

func TestAggregate_DriftedBucketsAndCountsServer(t *testing.T) {
	srvID := uuid.New()
	srv := dbq.DhcpServerDriftSummaryRow{ID: srvID, Name: "kea-1", FabricID: uuid.New(), Enabled: true}
	scopes := map[uuid.UUID][]dbq.DhcpScopeDriftStatusRow{
		srvID: {
			{ID: uuid.New(), DhcpServerID: srvID, LastDiffStatus: ptr("drifted")},
			{ID: uuid.New(), DhcpServerID: srvID, LastDiffStatus: ptr("in_sync")},
		},
	}
	fleet, _, summaries := Aggregate([]dbq.DhcpServerDriftSummaryRow{srv}, scopes, nil)
	if summaries[0].ScopeCounts["drifted"] != 1 {
		t.Errorf("drifted = %d, want 1", summaries[0].ScopeCounts["drifted"])
	}
	if fleet.ServersWithDrift != 1 {
		t.Errorf("ServersWithDrift = %d, want 1 (one server has drifted scopes)", fleet.ServersWithDrift)
	}
}

func TestAggregate_UnknownStatusGoesToErrorBucket(t *testing.T) {
	srvID := uuid.New()
	srv := dbq.DhcpServerDriftSummaryRow{ID: srvID, Name: "kea-1", FabricID: uuid.New(), Enabled: true}
	scopes := map[uuid.UUID][]dbq.DhcpScopeDriftStatusRow{
		srvID: {{ID: uuid.New(), DhcpServerID: srvID, LastDiffStatus: ptr("frobnicated")}},
	}
	_, _, summaries := Aggregate([]dbq.DhcpServerDriftSummaryRow{srv}, scopes, nil)
	if summaries[0].ScopeCounts["error"] != 1 {
		t.Errorf("unknown status must bucket into error, got %+v", summaries[0].ScopeCounts)
	}
}

func TestAggregate_PerFabricSliceAggregatesAcrossServers(t *testing.T) {
	fabricA, fabricB := uuid.New(), uuid.New()
	srv1 := dbq.DhcpServerDriftSummaryRow{ID: uuid.New(), Name: "a-1", FabricID: fabricA, Enabled: true}
	srv2 := dbq.DhcpServerDriftSummaryRow{ID: uuid.New(), Name: "a-2", FabricID: fabricA, Enabled: true}
	srv3 := dbq.DhcpServerDriftSummaryRow{ID: uuid.New(), Name: "b-1", FabricID: fabricB, Enabled: true}
	scopes := map[uuid.UUID][]dbq.DhcpScopeDriftStatusRow{
		srv1.ID: {{ID: uuid.New(), DhcpServerID: srv1.ID, LastDiffStatus: ptr("drifted")}},
		srv2.ID: {{ID: uuid.New(), DhcpServerID: srv2.ID, LastDiffStatus: ptr("in_sync")}},
		srv3.ID: {{ID: uuid.New(), DhcpServerID: srv3.ID, LastDiffStatus: ptr("drifted")}},
	}
	_, fabrics, _ := Aggregate(
		[]dbq.DhcpServerDriftSummaryRow{srv1, srv2, srv3},
		scopes, nil,
	)
	if len(fabrics) != 2 {
		t.Fatalf("fabrics = %d, want 2", len(fabrics))
	}
	byID := map[string]FabricSummary{}
	for _, f := range fabrics {
		byID[f.FabricID] = f
	}
	a := byID[fabricA.String()]
	if a.ServersTotal != 2 || a.ServersWithDrift != 1 {
		t.Errorf("fabric A: servers=%d drift=%d, want 2/1", a.ServersTotal, a.ServersWithDrift)
	}
	b := byID[fabricB.String()]
	if b.ServersTotal != 1 || b.ServersWithDrift != 1 {
		t.Errorf("fabric B: servers=%d drift=%d, want 1/1", b.ServersTotal, b.ServersWithDrift)
	}
}

func TestAggregate_AlertCountsPropagate(t *testing.T) {
	srvID := uuid.New()
	scopeID := uuid.New()
	srv := dbq.DhcpServerDriftSummaryRow{ID: srvID, Name: "kea-1", FabricID: uuid.New(), Enabled: true}
	scopes := map[uuid.UUID][]dbq.DhcpScopeDriftStatusRow{
		srvID: {{ID: scopeID, DhcpServerID: srvID, LastDiffStatus: ptr("drifted")}},
	}
	alerts := map[string]int{scopeID.String(): 1}
	fleet, fabrics, summaries := Aggregate([]dbq.DhcpServerDriftSummaryRow{srv}, scopes, alerts)
	if summaries[0].AlertsFiring != 1 {
		t.Errorf("server alerts = %d, want 1", summaries[0].AlertsFiring)
	}
	if fabrics[0].AlertsFiring != 1 {
		t.Errorf("fabric alerts = %d, want 1", fabrics[0].AlertsFiring)
	}
	if fleet.AlertsFiring != 1 {
		t.Errorf("fleet alerts = %d, want 1", fleet.AlertsFiring)
	}
}

func TestAlertCountsByScopeID_ParsesPrefix(t *testing.T) {
	scopeA := uuid.New().String()
	scopeB := uuid.New().String()
	keys := []string{
		"dhcp-drift:" + scopeA,
		"dhcp-drift:" + scopeB,
		"dhcp-drift:" + scopeA, // repeated → count climbs
		"other-prefix:irrelevant",
		"dhcp-drift:", // empty suffix → skipped
	}
	got := AlertCountsByScopeID(keys)
	if got[scopeA] != 2 {
		t.Errorf("scopeA count = %d, want 2", got[scopeA])
	}
	if got[scopeB] != 1 {
		t.Errorf("scopeB count = %d, want 1", got[scopeB])
	}
	if _, ok := got[""]; ok {
		t.Errorf("empty suffix must be skipped, got %v", got[""])
	}
	if _, ok := got["other-prefix:irrelevant"]; ok {
		t.Errorf("non-dhcp-drift prefix must be skipped")
	}
}

// Python's tz-aware UTC datetime renders as
// "2024-01-15T12:00:00.000000+00:00" via isoformat(). Go's default
// *time.Time marshal would emit "2024-01-15T12:00:00Z", breaking
// strict ISO-8601-with-offset consumers. The aggregator formats
// LastPushAt manually to match Python.
func TestServerSummary_LastPushAtMatchesPythonISOFormat(t *testing.T) {
	srvID := uuid.New()
	when := time.Date(2024, 1, 15, 12, 0, 0, 123456000, time.UTC)
	srv := dbq.DhcpServerDriftSummaryRow{
		ID: srvID, Name: "kea-1", FabricID: uuid.New(),
		Enabled: true, LastPushAt: &when,
	}
	_, _, summaries := Aggregate([]dbq.DhcpServerDriftSummaryRow{srv}, nil, nil)
	want := "2024-01-15T12:00:00.123456+00:00"
	if summaries[0].LastPushAt == nil || *summaries[0].LastPushAt != want {
		t.Errorf("LastPushAt = %v, want %q (Python isoformat parity)", summaries[0].LastPushAt, want)
	}
	body, err := json.Marshal(summaries[0])
	if err != nil {
		t.Fatal(err)
	}
	// The string carries +00:00 NOT Z. Catch a regression that
	// reverted the *string back to *time.Time.
	if !contains(body, `"last_push_at":"2024-01-15T12:00:00.123456+00:00"`) {
		t.Errorf("wire format wrong, got %s", body)
	}
}

func TestServerSummary_NullLastPushAtSerializesAsNull(t *testing.T) {
	srv := dbq.DhcpServerDriftSummaryRow{
		ID: uuid.New(), Name: "fresh", FabricID: uuid.New(),
		Enabled: true, LastPushAt: nil, LastPushStatus: nil,
	}
	_, _, summaries := Aggregate([]dbq.DhcpServerDriftSummaryRow{srv}, nil, nil)
	body, err := json.Marshal(summaries[0])
	if err != nil {
		t.Fatal(err)
	}
	if !contains(body, `"last_push_at":null`) {
		t.Errorf("nil LastPushAt must JSON to null, got %s", body)
	}
	if !contains(body, `"last_push_status":null`) {
		t.Errorf("nil LastPushStatus must JSON to null, got %s", body)
	}
}

// Per-fabric emission order must match server-iteration order so
// downstream UIs render fabrics deterministically. Catches a
// regression that swaps fabricOrder for map iteration.
func TestAggregate_FabricEmitOrderMatchesFirstServerSighting(t *testing.T) {
	fabricA, fabricB, fabricC := uuid.New(), uuid.New(), uuid.New()
	// Servers in this order: A, C, B, A. First-sighting order is
	// then A, C, B regardless of fabric UUID sort.
	servers := []dbq.DhcpServerDriftSummaryRow{
		{ID: uuid.New(), Name: "s1", FabricID: fabricA, Enabled: true},
		{ID: uuid.New(), Name: "s2", FabricID: fabricC, Enabled: true},
		{ID: uuid.New(), Name: "s3", FabricID: fabricB, Enabled: true},
		{ID: uuid.New(), Name: "s4", FabricID: fabricA, Enabled: true},
	}
	_, fabrics, _ := Aggregate(servers, nil, nil)
	want := []string{fabricA.String(), fabricC.String(), fabricB.String()}
	if len(fabrics) != 3 {
		t.Fatalf("fabric count = %d, want 3", len(fabrics))
	}
	for i, w := range want {
		if fabrics[i].FabricID != w {
			t.Errorf("fabric[%d] = %s, want %s (first-sighting order)", i, fabrics[i].FabricID, w)
		}
	}
}

// contains is a tiny string-arg shim over bytes.Contains so the
// assertions read naturally in the wire-format tests.
func contains(haystack []byte, needle string) bool {
	return bytes.Contains(haystack, []byte(needle))
}

func TestScopesByServer_IndexesCorrectly(t *testing.T) {
	srvA, srvB := uuid.New(), uuid.New()
	rows := []dbq.DhcpScopeDriftStatusRow{
		{ID: uuid.New(), DhcpServerID: srvA},
		{ID: uuid.New(), DhcpServerID: srvA},
		{ID: uuid.New(), DhcpServerID: srvB},
	}
	got := ScopesByServer(rows)
	if len(got[srvA]) != 2 || len(got[srvB]) != 1 {
		t.Errorf("index counts: A=%d B=%d, want 2/1", len(got[srvA]), len(got[srvB]))
	}
}
