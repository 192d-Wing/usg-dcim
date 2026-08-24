// Tests for PushAllScopes + PushDriftedScopes. The per-scope
// PushScope itself is exhaustively covered in push_test.go; the
// bulk tests focus on:
//
//   - List-query failure → batch error
//   - Empty list → Total=0 + zero counts
//   - Per-scope failures don't abort the batch
//   - Counts aggregate by Status
//   - PushAllScopes vs PushDriftedScopes route to different list queries
package push

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/kea"
)

// bulkFakeQ drives PushAllScopes / PushDriftedScopes. The enabled /
// drifted lists are independent so a test can distinguish which list
// the orchestrator queried. Each id in the enabled/drifted list maps
// to a pre-seeded DhcpScopeForPushRow + DhcpServerForPushRow.
type bulkFakeQ struct {
	enabled       []uuid.UUID
	enabledErr    error
	drifted       []uuid.UUID
	driftedErr    error
	scopes        map[uuid.UUID]dbq.GetDhcpScopeForPushRow
	servers       map[uuid.UUID]dbq.GetDhcpServerForPushRow
	templates     map[uuid.UUID]dbq.DhcpScopeTemplate
	claimedKeaIDs map[uuid.UUID][]int32

	enabledCalls int
	driftedCalls int
}

func (f *bulkFakeQ) ListEnabledScopeIDsForServer(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	f.enabledCalls++
	return f.enabled, f.enabledErr
}
func (f *bulkFakeQ) ListDriftedScopeIDsForServer(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	f.driftedCalls++
	return f.drifted, f.driftedErr
}
func (f *bulkFakeQ) GetDhcpScopeForPush(_ context.Context, id uuid.UUID) (dbq.GetDhcpScopeForPushRow, error) {
	r, ok := f.scopes[id]
	if !ok {
		return dbq.GetDhcpScopeForPushRow{}, pgx.ErrNoRows
	}
	return r, nil
}
func (f *bulkFakeQ) GetDhcpServerForPush(_ context.Context, id uuid.UUID) (dbq.GetDhcpServerForPushRow, error) {
	r, ok := f.servers[id]
	if !ok {
		return dbq.GetDhcpServerForPushRow{}, pgx.ErrNoRows
	}
	return r, nil
}
func (f *bulkFakeQ) GetDhcpScopeTemplateForPush(_ context.Context, id uuid.UUID) (dbq.DhcpScopeTemplate, error) {
	r, ok := f.templates[id]
	if !ok {
		return dbq.DhcpScopeTemplate{}, pgx.ErrNoRows
	}
	return r, nil
}
func (f *bulkFakeQ) ListKeaSubnetIDsForServer(_ context.Context, sid uuid.UUID) ([]int32, error) {
	return append([]int32(nil), f.claimedKeaIDs[sid]...), nil
}
func (f *bulkFakeQ) UpdateDhcpScopeKeaSubnetID(_ context.Context, arg dbq.UpdateDhcpScopeKeaSubnetIDParams) error {
	s := f.scopes[arg.ID]
	s.KeaSubnetID = arg.KeaSubnetID
	f.scopes[arg.ID] = s
	return nil
}
func (f *bulkFakeQ) UpdateDhcpScopeAfterSuccessfulPush(_ context.Context, _ uuid.UUID) error {
	return nil
}
func (f *bulkFakeQ) UpdateDhcpServerLastPush(_ context.Context, _ dbq.UpdateDhcpServerLastPushParams) error {
	return nil
}
func (f *bulkFakeQ) InsertDhcpScopePushHistory(_ context.Context, _ dbq.InsertDhcpScopePushHistoryParams) error {
	return nil
}

// seedScope adds a v4 enabled scope keyed off the server id. Returns
// the scope id so tests can build enabled/drifted slices.
func seedScope(f *bulkFakeQ, serverID uuid.UUID) uuid.UUID {
	scopeID := uuid.New()
	f.scopes[scopeID] = dbq.GetDhcpScopeForPushRow{
		ID: scopeID, DhcpServerID: serverID, IPFamily: 4,
		Prefix: "10.0.0.0/24",
		PoolsJSON: json.RawMessage(`[]`), PdPoolsJSON: json.RawMessage(`[]`),
		OptionsJSON: json.RawMessage(`[]`), ReservationsJSON: json.RawMessage(`[]`),
		Enabled: true,
	}
	return scopeID
}

func newBulkFake(serverID uuid.UUID) *bulkFakeQ {
	return &bulkFakeQ{
		scopes:    map[uuid.UUID]dbq.GetDhcpScopeForPushRow{},
		servers:   map[uuid.UUID]dbq.GetDhcpServerForPushRow{serverID: {ID: serverID, KeaURL: "http://kea", Enabled: true}},
		templates: map[uuid.UUID]dbq.DhcpScopeTemplate{},
		claimedKeaIDs: map[uuid.UUID][]int32{},
	}
}

func TestPushAllScopes_EmptyServer_ReturnsZeroCounts(t *testing.T) {
	serverID := uuid.New()
	f := newBulkFake(serverID)
	r, err := PushAllScopes(context.Background(), f, builderReturning(&fakeKea{}), serverID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 0 || len(r.Results) != 0 {
		t.Errorf("Total=%d results=%d, want 0/0", r.Total, len(r.Results))
	}
	// Counts must carry every known status with zero, not be nil/empty —
	// matches Python's _tally(dict.fromkeys(known, 0)) initial state.
	for _, s := range pushStatuses {
		if got, ok := r.Counts[string(s)]; !ok || got != 0 {
			t.Errorf("Counts[%s] = %d ok=%v, want 0", s, got, ok)
		}
	}
	if f.enabledCalls != 1 {
		t.Errorf("enabledCalls = %d, want 1", f.enabledCalls)
	}
	if f.driftedCalls != 0 {
		t.Errorf("PushAll must not call ListDrifted, got %d calls", f.driftedCalls)
	}
}

func TestPushAllScopes_ListError_FailsBatch(t *testing.T) {
	serverID := uuid.New()
	f := newBulkFake(serverID)
	f.enabledErr = errors.New("connection refused")
	_, err := PushAllScopes(context.Background(), f, builderReturning(&fakeKea{}), serverID)
	if err == nil {
		t.Fatal("want non-nil error on list failure")
	}
}

func TestPushAllScopes_MultipleScopes_PushesEachAndAggregates(t *testing.T) {
	serverID := uuid.New()
	f := newBulkFake(serverID)
	id1 := seedScope(f, serverID)
	id2 := seedScope(f, serverID)
	f.enabled = []uuid.UUID{id1, id2}
	fk := &fakeKea{subnetResp: []byte(`[{"result":0,"text":"ok"}]`)}
	r, err := PushAllScopes(context.Background(), f, builderReturning(fk), serverID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 2 || len(r.Results) != 2 {
		t.Fatalf("Total=%d results=%d, want 2/2", r.Total, len(r.Results))
	}
	if r.Counts[string(kea.StatusOK)] != 2 {
		t.Errorf("Counts[ok] = %d, want 2", r.Counts[string(kea.StatusOK)])
	}
	// Result order matches list order so dashboards can render row N.
	if r.Results[0].ScopeID != id1 || r.Results[1].ScopeID != id2 {
		t.Errorf("result order mismatch: %v vs [%s %s]", r.Results, id1, id2)
	}
	if r.ServerID != serverID.String() {
		t.Errorf("ServerID = %q, want %q", r.ServerID, serverID.String())
	}
}

func TestPushAllScopes_PartialFailure_BatchContinues(t *testing.T) {
	serverID := uuid.New()
	f := newBulkFake(serverID)
	id1 := seedScope(f, serverID)
	id2 := seedScope(f, serverID)
	// id2 will surface as a result with Status=error because the
	// scope's server was deleted between list and push.
	delete(f.servers, serverID) // both lookups hit this same id
	f.enabled = []uuid.UUID{id1, id2}
	// Reseed for id1 only via a different server id wouldn't work
	// because both scopes reference serverID; instead seed it back so
	// id1 succeeds, leaving id2's per-scope failure to come from a
	// deleted scope row.
	f.servers[serverID] = dbq.GetDhcpServerForPushRow{ID: serverID, KeaURL: "http://kea", Enabled: true}
	delete(f.scopes, id2)
	fk := &fakeKea{subnetResp: []byte(`[{"result":0,"text":"ok"}]`)}
	r, err := PushAllScopes(context.Background(), f, builderReturning(fk), serverID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 2 {
		t.Fatalf("Total = %d, want 2 (per-scope failure stays in batch)", r.Total)
	}
	if r.Counts[string(kea.StatusOK)] != 1 || r.Counts[string(kea.StatusError)] != 1 {
		t.Errorf("counts = %+v, want ok=1 error=1", r.Counts)
	}
}

func TestPushDriftedScopes_RoutesToDriftedQuery(t *testing.T) {
	serverID := uuid.New()
	f := newBulkFake(serverID)
	id := seedScope(f, serverID)
	f.drifted = []uuid.UUID{id}
	fk := &fakeKea{subnetResp: []byte(`[{"result":0,"text":"ok"}]`)}
	r, err := PushDriftedScopes(context.Background(), f, builderReturning(fk), serverID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 1 {
		t.Errorf("Total = %d, want 1", r.Total)
	}
	if f.driftedCalls != 1 {
		t.Errorf("driftedCalls = %d, want 1", f.driftedCalls)
	}
	if f.enabledCalls != 0 {
		t.Errorf("PushDrifted must not call ListEnabled, got %d", f.enabledCalls)
	}
}

func TestPushDriftedScopes_EmptyDriftedSet_ReturnsEmptyReport(t *testing.T) {
	serverID := uuid.New()
	f := newBulkFake(serverID)
	r, err := PushDriftedScopes(context.Background(), f, builderReturning(&fakeKea{}), serverID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 0 {
		t.Errorf("Total = %d, want 0", r.Total)
	}
	if r.Counts[string(kea.StatusOK)] != 0 {
		t.Errorf("Counts[ok] = %d, want 0", r.Counts[string(kea.StatusOK)])
	}
}

// Fatal error from PushScope (DB write failure during recordOutcome)
// must abort the batch with a wrapped error — NOT silently fold into
// the results array. Otherwise an infrastructure failure looks like
// per-scope refusals and operators don't escalate.
type fatalPushFakeQ struct {
	*bulkFakeQ
	historyErr error
}

func (f *fatalPushFakeQ) InsertDhcpScopePushHistory(_ context.Context, _ dbq.InsertDhcpScopePushHistoryParams) error {
	return f.historyErr
}

func TestPushAllScopes_FatalScopeError_AbortsBatch(t *testing.T) {
	serverID := uuid.New()
	inner := newBulkFake(serverID)
	id := seedScope(inner, serverID)
	inner.enabled = []uuid.UUID{id}
	f := &fatalPushFakeQ{bulkFakeQ: inner, historyErr: errors.New("db down")}
	fk := &fakeKea{subnetResp: []byte(`[{"result":0,"text":"ok"}]`)}
	_, err := PushAllScopes(context.Background(), f, builderReturning(fk), serverID)
	if err == nil {
		t.Fatal("want non-nil error when PushScope returns fatal err")
	}
	// Error must wrap the scope id so operators know which scope hit
	// the infrastructure failure.
	if !strings.Contains(err.Error(), id.String()) {
		t.Errorf("err = %q, want wrapped scope id %s", err.Error(), id)
	}
}

func TestTallyPushStatuses_UnknownGoesToOther(t *testing.T) {
	// Defensive: a future status value drift should land in "other"
	// rather than silently disappear.
	results := []Result{
		{Status: kea.StatusOK},
		{Status: kea.Status("frobnicated")},
	}
	counts := tallyPushStatuses(results)
	if counts["other"] != 1 {
		t.Errorf("other = %d, want 1", counts["other"])
	}
	if counts[string(kea.StatusOK)] != 1 {
		t.Errorf("ok = %d, want 1", counts[string(kea.StatusOK)])
	}
}
