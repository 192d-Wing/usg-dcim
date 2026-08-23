// Tests for the dhcp_drift_check scheduler job. The diff.DiffAllScopes
// orchestrator is exhaustively covered in internal/dhcp/diff/bulk_test.go;
// these tests focus on the cron driver's concerns:
//
//   - nil Querier → error before any DB call
//   - List failure → wrapped error, no per-server work
//   - Empty fleet → zero counts, no per-server logs
//   - Per-server failure logged + loop continues
//   - Context cancellation between servers returns partial counts
//   - Aggregate counts sum per-server counts correctly
package dhcpdriftcheck

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/diff"
)

// fakeQ stitches together the methods diff.DiffAllScopes + this job
// reach for. Per-server scope lists drive the diff loop; the scopes
// map seeds the never_pushed short-circuit path so the test doesn't
// need to stand up a fake Kea client.
type fakeQ struct {
	serverIDs []uuid.UUID
	listErr   error

	allByServer map[uuid.UUID][]dbq.ListAllScopeIDsAndPriorDriftForServerRow
	allErrFor   map[uuid.UUID]error
	scopes      map[uuid.UUID]dbq.GetDhcpScopeForPushRow
	writes      map[uuid.UUID]dbq.WriteDhcpScopeDiffStateParams
}

func (f *fakeQ) ListEnabledDhcpServerIDs(_ context.Context) ([]uuid.UUID, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.serverIDs, nil
}
func (f *fakeQ) ListAllScopeIDsAndPriorDriftForServer(_ context.Context, sid uuid.UUID) ([]dbq.ListAllScopeIDsAndPriorDriftForServerRow, error) {
	if err, ok := f.allErrFor[sid]; ok {
		return nil, err
	}
	return f.allByServer[sid], nil
}
func (f *fakeQ) GetDhcpScopeForPush(_ context.Context, id uuid.UUID) (dbq.GetDhcpScopeForPushRow, error) {
	r, ok := f.scopes[id]
	if !ok {
		return dbq.GetDhcpScopeForPushRow{}, pgx.ErrNoRows
	}
	return r, nil
}

// Server / template fetches are never reached by tests here — every
// fixture scope short-circuits at the never_pushed branch in
// DiffScope. Sentinel ErrNoRows so a future test reaching the full
// diff path fails loudly. Same posture diff/bulk_test.go uses.
func (f *fakeQ) GetDhcpServerForPush(_ context.Context, _ uuid.UUID) (dbq.GetDhcpServerForPushRow, error) {
	return dbq.GetDhcpServerForPushRow{}, pgx.ErrNoRows
}
func (f *fakeQ) GetDhcpScopeTemplateForPush(_ context.Context, _ uuid.UUID) (dbq.DhcpScopeTemplate, error) {
	return dbq.DhcpScopeTemplate{}, pgx.ErrNoRows
}
func (f *fakeQ) WriteDhcpScopeDiffState(_ context.Context, arg dbq.WriteDhcpScopeDiffStateParams) error {
	if f.writes == nil {
		f.writes = map[uuid.UUID]dbq.WriteDhcpScopeDiffStateParams{}
	}
	f.writes[arg.ID] = arg
	return nil
}

// seedScope inserts a never-pushed v4 scope so the DiffScope short-
// circuit fires without needing a Kea client.
func seedScope(f *fakeQ) uuid.UUID {
	id := uuid.New()
	f.scopes[id] = dbq.GetDhcpScopeForPushRow{
		ID: id, IPFamily: 4, Prefix: "10.0.0.0/24",
		PoolsJSON: []byte(`[]`), PdPoolsJSON: []byte(`[]`),
		OptionsJSON: []byte(`[]`), ReservationsJSON: []byte(`[]`),
		Enabled: true,
	}
	return id
}

func newFake() *fakeQ {
	return &fakeQ{
		allByServer: map[uuid.UUID][]dbq.ListAllScopeIDsAndPriorDriftForServerRow{},
		scopes:      map[uuid.UUID]dbq.GetDhcpScopeForPushRow{},
	}
}

func TestRun_NilQuerier_Rejected(t *testing.T) {
	j := &Job{}
	if _, err := j.Run(context.Background()); err == nil {
		t.Error("expected error for nil Q")
	}
}

func TestRun_ListErr_Wrapped(t *testing.T) {
	f := newFake()
	f.listErr = errors.New("connection refused")
	j := &Job{Q: f}
	_, err := j.Run(context.Background())
	if err == nil {
		t.Fatal("want error")
	}
	// Wrapped so the harness can switch on errors.Is — the original
	// "connection refused" must travel through unmangled.
	if !errors.Is(err, f.listErr) {
		t.Errorf("err chain doesn't wrap listErr: %v", err)
	}
}

func TestRun_EmptyFleet_ZeroAggregate(t *testing.T) {
	f := newFake()
	j := &Job{Q: f}
	out, err := j.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"servers", "errors", "total_scopes", "total_drifted", "total_transitions"} {
		if v, ok := out[key].(int); !ok || v != 0 {
			t.Errorf("out[%q] = %v, want 0", key, out[key])
		}
	}
}

func TestRun_PerServerError_LoopContinues(t *testing.T) {
	f := newFake()
	srv1, srv2 := uuid.New(), uuid.New()
	f.serverIDs = []uuid.UUID{srv1, srv2}
	f.allErrFor = map[uuid.UUID]error{srv1: errors.New("kea unreachable")}
	scopeID := seedScope(f)
	f.allByServer[srv2] = []dbq.ListAllScopeIDsAndPriorDriftForServerRow{
		{ID: scopeID, Prefix: "10.0.0.0/24", LastDiffStatus: nil},
	}
	j := &Job{Q: f}
	out, err := j.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out["servers"].(int) != 2 {
		t.Errorf("servers = %v, want 2", out["servers"])
	}
	if out["errors"].(int) != 1 {
		t.Errorf("errors = %v, want 1", out["errors"])
	}
	// srv2's one never_pushed scope still got counted into the
	// aggregate — error on srv1 must not zero out srv2's results.
	if out["total_scopes"].(int) != 1 {
		t.Errorf("total_scopes = %v, want 1", out["total_scopes"])
	}
	// Cold-start transition (NULL → never_pushed) emits one transition.
	if out["total_transitions"].(int) != 1 {
		t.Errorf("total_transitions = %v, want 1", out["total_transitions"])
	}
}

func TestRun_CountsAggregateAcrossServers(t *testing.T) {
	f := newFake()
	srv1, srv2 := uuid.New(), uuid.New()
	f.serverIDs = []uuid.UUID{srv1, srv2}
	for _, sid := range []uuid.UUID{srv1, srv2} {
		scopeID := seedScope(f)
		f.allByServer[sid] = []dbq.ListAllScopeIDsAndPriorDriftForServerRow{
			{ID: scopeID, Prefix: "10.0.0.0/24", LastDiffStatus: nil},
		}
	}
	j := &Job{Q: f}
	out, err := j.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out["total_scopes"].(int) != 2 {
		t.Errorf("total_scopes = %v, want 2 (1 per server)", out["total_scopes"])
	}
	if out["total_transitions"].(int) != 2 {
		t.Errorf("total_transitions = %v, want 2", out["total_transitions"])
	}
	// No scope was drifted (all never_pushed) → total_drifted=0.
	if out["total_drifted"].(int) != 0 {
		t.Errorf("total_drifted = %v, want 0", out["total_drifted"])
	}
}

func TestRun_ContextCancellation_BailsWithPartialCounts(t *testing.T) {
	f := newFake()
	// Many servers; cancel before any iteration runs.
	f.serverIDs = []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	j := &Job{Q: f}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := j.Run(ctx)
	// Cancellation bails cleanly — no error returned, partial counts
	// in the result map. Matches the bundle rerender job's posture.
	if err != nil {
		t.Errorf("cancellation must return nil err, got %v", err)
	}
	// Servers was incremented by 0 because the ctx.Err check fires
	// before the servers++ at the top of the loop body.
	if out["servers"].(int) != 0 {
		t.Errorf("servers on immediate cancel = %v, want 0", out["servers"])
	}
}

// Mid-loop cancellation: complete one server's work, then cancel,
// and the partial counts must reflect the work that ran before the
// cancel propagated. Catches a regression where the ctx.Err guard
// moves to the bottom of the loop body (would 1-index off servers
// after a partial drain).
func TestRun_ContextCancellation_MidLoop_KeepsPartialCounts(t *testing.T) {
	f := newFake()
	srv1, srv2 := uuid.New(), uuid.New()
	f.serverIDs = []uuid.UUID{srv1, srv2}
	// srv1 has one scope so it completes; srv2's list lookup
	// returns ctx.Canceled (simulating the cancel arriving mid-
	// DiffAllScopes).
	scopeID := seedScope(f)
	f.allByServer[srv1] = []dbq.ListAllScopeIDsAndPriorDriftForServerRow{
		{ID: scopeID, Prefix: "10.0.0.0/24", LastDiffStatus: nil},
	}
	f.allErrFor = map[uuid.UUID]error{srv2: context.Canceled}
	j := &Job{Q: f}
	out, err := j.Run(context.Background())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	// srv1 finished → servers=1, total_scopes=1, no errCount bump
	// for srv2's cancellation.
	if got := out["servers"].(int); got != 1 {
		t.Errorf("servers = %d, want 1 (cancel must not count srv2)", got)
	}
	if got := out["total_scopes"].(int); got != 1 {
		t.Errorf("total_scopes = %d, want 1 (srv1's scope must be preserved)", got)
	}
	if got := out["errors"].(int); got != 0 {
		t.Errorf("errors = %d, want 0 (cancel must not bucket as failure)", got)
	}
}

// driftedFake injects a pre-seeded DiffAllScopes outcome by stubbing
// the diff orchestrator's narrow Querier with a scope that will
// reach the "drifted" branch: KeaSubnetID non-nil, server present,
// Kea stub returns a subnet whose fields differ from the DCIM render.
// Used to exercise totalDrifted aggregation — without this, every
// fixture scope is never_pushed and total_drifted is always 0,
// hiding typos like "totalDrifted += report.Counts[StatusInSync]".
type driftedFake struct {
	*fakeQ
	server     dbq.GetDhcpServerForPushRow
	keaSubnet4 []byte
}

func (f *driftedFake) GetDhcpServerForPush(_ context.Context, _ uuid.UUID) (dbq.GetDhcpServerForPushRow, error) {
	return f.server, nil
}

type driftedKea struct{ resp []byte }

func (k driftedKea) Subnet4Get(_ context.Context, _ int64) ([]byte, error) { return k.resp, nil }
func (k driftedKea) Subnet6Get(_ context.Context, _ int64) ([]byte, error) { return k.resp, nil }

func TestRun_AggregateCountsDriftedAcrossServers(t *testing.T) {
	srvID := uuid.New()
	keaID := int32(1)
	scopeID := uuid.New()
	base := newFake()
	base.serverIDs = []uuid.UUID{srvID}
	base.scopes[scopeID] = dbq.GetDhcpScopeForPushRow{
		ID: scopeID, DhcpServerID: srvID, IPFamily: 4,
		Prefix: "10.0.0.0/24", KeaSubnetID: &keaID,
		PoolsJSON:    []byte(`[{"first":"10.0.0.10","last":"10.0.0.250"}]`),
		PdPoolsJSON:  []byte(`[]`),
		OptionsJSON:  []byte(`[]`),
		ReservationsJSON: []byte(`[]`),
		Enabled: true,
	}
	base.allByServer[srvID] = []dbq.ListAllScopeIDsAndPriorDriftForServerRow{
		{ID: scopeID, Prefix: "10.0.0.0/24", LastDiffStatus: nil},
	}
	// Kea returns a subnet with a different pool first/last so the
	// per-key diff lands in the "pools" multiset and the status flips
	// to drifted. The shape is what ExtractKeaSubnet expects:
	// [{"result":0,"arguments":{"subnet4":[{...}]}}].
	keaResp := []byte(`[{"result":0,"arguments":{"subnet4":[{"id":1,"subnet":"10.0.0.0/24","pools":[{"first":"10.0.0.50","last":"10.0.0.200"}]}]}}]`)
	f := &driftedFake{
		fakeQ:      base,
		server:     dbq.GetDhcpServerForPushRow{ID: srvID, KeaURL: "stub", Enabled: true},
		keaSubnet4: keaResp,
	}
	j := &Job{
		Q: f,
		KeaBuilder: func(_ dbq.GetDhcpServerForPushRow) diff.KeaClient {
			return driftedKea{resp: keaResp}
		},
	}
	out, err := j.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := out["total_drifted"].(int); got != 1 {
		t.Errorf("total_drifted = %d, want 1 — drifted-status fixture didn't aggregate", got)
	}
	if got := out["total_scopes"].(int); got != 1 {
		t.Errorf("total_scopes = %d, want 1", got)
	}
}

// Compile-time check that the test fake actually satisfies the
// production Querier interface — catches type drift if the job's
// Querier surface widens or the diff.BulkQuerier embeds new methods.
var _ Querier = (*fakeQ)(nil)
