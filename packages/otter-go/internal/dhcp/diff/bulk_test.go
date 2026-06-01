// Tests for DiffAllScopes. The per-scope DiffScope is exhaustively
// covered in diff_test.go; the bulk tests focus on:
//
//   - List-query failure → batch error
//   - Empty server → Total=0 + zero counts + empty transitions
//   - Multi-scope: per-scope DiffScope is invoked, per-scope persist
//     writes happen, counts aggregate by Status
//   - Transitions list: prior_status != new_status emits one row
//     per change; cold-start (NULL prior) treats every result as a
//     transition (None → status)
package diff

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// bulkFakeQ drives DiffAllScopes. Only the methods the never_pushed
// short-circuit hits are stubbed here — DiffScope returns at diff.go's
// "scope.KeaSubnetID == nil" branch before any server/template fetch,
// so GetDhcpServerForPush / GetDhcpScopeTemplateForPush would be dead
// code if defined. Tests that need the full diff path (drifted, Kea-
// missing, parse error) belong in diff_test.go where the per-scope
// fakeQ already covers them.
type bulkFakeQ struct {
	all      []dbq.DhcpScopeIDAndPriorDriftRow
	allErr   error
	scopes   map[uuid.UUID]dbq.DhcpScopeForPushRow
	writes   map[uuid.UUID]dbq.WriteDhcpScopeDiffStateParams
	allCalls int
}

func (f *bulkFakeQ) ListAllScopeIDsAndPriorDriftForServer(_ context.Context, _ uuid.UUID) ([]dbq.DhcpScopeIDAndPriorDriftRow, error) {
	f.allCalls++
	return f.all, f.allErr
}
func (f *bulkFakeQ) GetDhcpScopeForPush(_ context.Context, id uuid.UUID) (dbq.DhcpScopeForPushRow, error) {
	r, ok := f.scopes[id]
	if !ok {
		return dbq.DhcpScopeForPushRow{}, pgx.ErrNoRows
	}
	return r, nil
}

// GetDhcpServerForPush + GetDhcpScopeTemplateForPush are required by
// the diff.Querier interface but never invoked from these tests: the
// only DiffScope path exercised here is the never_pushed short-
// circuit (Kea-side is unreachable before either lookup fires). They
// return ErrNoRows so any future test reaching the full diff path
// fails loudly rather than silently navigating into a nil row.
func (f *bulkFakeQ) GetDhcpServerForPush(_ context.Context, _ uuid.UUID) (dbq.DhcpServerForPushRow, error) {
	return dbq.DhcpServerForPushRow{}, pgx.ErrNoRows
}
func (f *bulkFakeQ) GetDhcpScopeTemplateForPush(_ context.Context, _ uuid.UUID) (dbq.DhcpScopeTemplate, error) {
	return dbq.DhcpScopeTemplate{}, pgx.ErrNoRows
}
func (f *bulkFakeQ) WriteDhcpScopeDiffState(_ context.Context, arg dbq.WriteDhcpScopeDiffStateParams) error {
	f.writes[arg.ID] = arg
	return nil
}

func newBulkDiffFake(_ uuid.UUID) *bulkFakeQ {
	return &bulkFakeQ{
		scopes: map[uuid.UUID]dbq.DhcpScopeForPushRow{},
		writes: map[uuid.UUID]dbq.WriteDhcpScopeDiffStateParams{},
	}
}

func seedNeverPushedScope(f *bulkFakeQ, serverID uuid.UUID, prefix string) uuid.UUID {
	id := uuid.New()
	f.scopes[id] = dbq.DhcpScopeForPushRow{
		ID: id, DhcpServerID: serverID, IPFamily: 4, Prefix: prefix,
		PoolsJSON: json.RawMessage(`[]`), PdPoolsJSON: json.RawMessage(`[]`),
		OptionsJSON: json.RawMessage(`[]`), ReservationsJSON: json.RawMessage(`[]`),
		Enabled: true,
		// KeaSubnetID nil → DiffScope short-circuits to never_pushed
	}
	return id
}

func builderForBulk() KeaClientBuilder {
	return func(_ dbq.DhcpServerForPushRow) KeaClient {
		// never_pushed scopes never reach the Kea RPC, so the stub
		// methods can panic — calling them is a test bug.
		return panicKea{}
	}
}

type panicKea struct{}

func (panicKea) Subnet4Get(context.Context, int64) ([]byte, error) {
	panic("never_pushed scope must not hit Kea")
}
func (panicKea) Subnet6Get(context.Context, int64) ([]byte, error) {
	panic("never_pushed scope must not hit Kea")
}

func TestDiffAllScopes_EmptyServer_ReturnsZeroCountsAndEmptyTransitions(t *testing.T) {
	serverID := uuid.New()
	f := newBulkDiffFake(serverID)
	r, err := DiffAllScopes(context.Background(), f, builderForBulk(), serverID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 0 || len(r.Results) != 0 {
		t.Errorf("Total=%d results=%d", r.Total, len(r.Results))
	}
	if r.Transitions == nil || len(r.Transitions) != 0 {
		t.Errorf("Transitions = %v, want empty (non-nil)", r.Transitions)
	}
	for _, s := range diffStatuses {
		if got, ok := r.Counts[string(s)]; !ok || got != 0 {
			t.Errorf("Counts[%s] = %d ok=%v, want 0", s, got, ok)
		}
	}
}

func TestDiffAllScopes_ListError_FailsBatch(t *testing.T) {
	serverID := uuid.New()
	f := newBulkDiffFake(serverID)
	f.allErr = errors.New("connection refused")
	_, err := DiffAllScopes(context.Background(), f, builderForBulk(), serverID)
	if err == nil {
		t.Fatal("want non-nil error on list failure")
	}
}

func TestDiffAllScopes_ColdStart_EveryResultIsATransition(t *testing.T) {
	// Cold start: every scope has NULL prior status, so every result
	// transitions None → its actual status. Matches Python's
	// `if prior_status != result.status` semantics where None != any
	// non-empty string.
	serverID := uuid.New()
	f := newBulkDiffFake(serverID)
	id1 := seedNeverPushedScope(f, serverID, "10.0.0.0/24")
	id2 := seedNeverPushedScope(f, serverID, "10.0.1.0/24")
	f.all = []dbq.DhcpScopeIDAndPriorDriftRow{
		{ID: id1, Prefix: "10.0.0.0/24", LastDiffStatus: nil},
		{ID: id2, Prefix: "10.0.1.0/24", LastDiffStatus: nil},
	}
	r, err := DiffAllScopes(context.Background(), f, builderForBulk(), serverID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 2 {
		t.Fatalf("Total = %d, want 2", r.Total)
	}
	if r.Counts[string(StatusNeverPushed)] != 2 {
		t.Errorf("Counts[never_pushed] = %d, want 2", r.Counts[string(StatusNeverPushed)])
	}
	if len(r.Transitions) != 2 {
		t.Fatalf("Transitions = %d, want 2", len(r.Transitions))
	}
	wantPrefixes := map[string]string{id1.String(): "10.0.0.0/24", id2.String(): "10.0.1.0/24"}
	for i, tr := range r.Transitions {
		if tr.FromStatus != nil {
			t.Errorf("transition[%d].FromStatus = %v, want nil (cold start)", i, *tr.FromStatus)
		}
		if tr.ToStatus != string(StatusNeverPushed) {
			t.Errorf("transition[%d].ToStatus = %q, want never_pushed", i, tr.ToStatus)
		}
		// Python sources prefix from scope.prefix (services/dhcp_push.py:1111);
		// even on never_pushed (where DCIMSubnet is nil) the transition
		// carries the prefix. The Go fix projects prefix in the list
		// query so this stays true.
		if tr.Prefix != wantPrefixes[tr.ScopeID] {
			t.Errorf("transition[%d].Prefix = %q, want %q", i, tr.Prefix, wantPrefixes[tr.ScopeID])
		}
	}
}

// Mixed prior_status: real second/third cron run shape — some scopes
// have a prior status set, some don't. Catches a regression where
// statusEqual's nil-handling gets inverted.
func TestDiffAllScopes_MixedPrior_OnlyChangedEmitTransitions(t *testing.T) {
	serverID := uuid.New()
	f := newBulkDiffFake(serverID)
	id1 := seedNeverPushedScope(f, serverID, "10.0.0.0/24") // nil prior → emits
	id2 := seedNeverPushedScope(f, serverID, "10.0.1.0/24") // prior=never_pushed → no transition
	id3 := seedNeverPushedScope(f, serverID, "10.0.2.0/24") // prior=in_sync → transition to never_pushed
	priorMatching := string(StatusNeverPushed)
	priorOther := string(StatusInSync)
	f.all = []dbq.DhcpScopeIDAndPriorDriftRow{
		{ID: id1, Prefix: "10.0.0.0/24", LastDiffStatus: nil},
		{ID: id2, Prefix: "10.0.1.0/24", LastDiffStatus: &priorMatching},
		{ID: id3, Prefix: "10.0.2.0/24", LastDiffStatus: &priorOther},
	}
	r, err := DiffAllScopes(context.Background(), f, builderForBulk(), serverID)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Transitions) != 2 {
		t.Fatalf("transitions = %d, want 2 (id1 cold-start, id3 changed)", len(r.Transitions))
	}
	seen := map[string]bool{}
	for _, tr := range r.Transitions {
		seen[tr.ScopeID] = true
	}
	if !seen[id1.String()] {
		t.Errorf("id1 (nil prior) must emit a transition")
	}
	if seen[id2.String()] {
		t.Errorf("id2 (matching prior) must NOT emit a transition")
	}
	if !seen[id3.String()] {
		t.Errorf("id3 (changed prior) must emit a transition")
	}
}

func TestDiffAllScopes_NoChange_EmitsNoTransitions(t *testing.T) {
	// Every prior == current → empty transitions list. Real ops
	// case: cron runs every 5min, no drift since last run.
	serverID := uuid.New()
	f := newBulkDiffFake(serverID)
	id := seedNeverPushedScope(f, serverID, "10.0.0.0/24")
	prior := string(StatusNeverPushed)
	f.all = []dbq.DhcpScopeIDAndPriorDriftRow{{ID: id, LastDiffStatus: &prior}}
	r, err := DiffAllScopes(context.Background(), f, builderForBulk(), serverID)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Transitions) != 0 {
		t.Errorf("Transitions = %v, want empty (no change)", r.Transitions)
	}
}

func TestDiffAllScopes_PersistDiffStateCalledPerScope(t *testing.T) {
	serverID := uuid.New()
	f := newBulkDiffFake(serverID)
	id1 := seedNeverPushedScope(f, serverID, "10.0.0.0/24")
	id2 := seedNeverPushedScope(f, serverID, "10.0.1.0/24")
	f.all = []dbq.DhcpScopeIDAndPriorDriftRow{{ID: id1}, {ID: id2}}
	_, err := DiffAllScopes(context.Background(), f, builderForBulk(), serverID)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 2 {
		t.Fatalf("persist writes = %d, want 2", len(f.writes))
	}
	for id, w := range f.writes {
		if w.LastDiffStatus != string(StatusNeverPushed) {
			t.Errorf("write[%s].status = %q, want never_pushed", id, w.LastDiffStatus)
		}
	}
}

func TestDiffAllScopes_TotalAndServerIDMatchPython(t *testing.T) {
	serverID := uuid.New()
	f := newBulkDiffFake(serverID)
	id := seedNeverPushedScope(f, serverID, "10.0.0.0/24")
	f.all = []dbq.DhcpScopeIDAndPriorDriftRow{{ID: id}}
	r, err := DiffAllScopes(context.Background(), f, builderForBulk(), serverID)
	if err != nil {
		t.Fatal(err)
	}
	if r.ServerID != serverID.String() {
		t.Errorf("ServerID = %q, want %q", r.ServerID, serverID.String())
	}
	if r.Total != 1 {
		t.Errorf("Total = %d, want 1", r.Total)
	}
}

func TestStatusEqual_NilAndEmptyAreEqual(t *testing.T) {
	// Defensive: an empty-string current with nil prior should
	// be equal (both represent "unknown"), so no spurious
	// transition fires. The producer never emits "" so this is
	// belt-and-braces.
	if !statusEqual(nil, "") {
		t.Errorf("statusEqual(nil, \"\") = false, want true")
	}
	if statusEqual(nil, "in_sync") {
		t.Errorf("statusEqual(nil, in_sync) = true, want false")
	}
}

func TestTallyDiffStatuses_UnknownGoesToOther(t *testing.T) {
	results := []Result{
		{Status: StatusInSync},
		{Status: Status("frobnicated")},
	}
	counts := tallyDiffStatuses(results)
	if counts["other"] != 1 {
		t.Errorf("other = %d, want 1", counts["other"])
	}
	if counts[string(StatusInSync)] != 1 {
		t.Errorf("in_sync = %d, want 1", counts[string(StatusInSync)])
	}
}
