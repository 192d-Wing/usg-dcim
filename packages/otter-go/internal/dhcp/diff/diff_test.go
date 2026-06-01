package diff

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

type fakeQ struct {
	scope         dbq.DhcpScopeForPushRow
	scopeErr      error
	server        dbq.DhcpServerForPushRow
	serverErr     error
	template      *dbq.DhcpScopeTemplate
	diffWriteArgs *dbq.WriteDhcpScopeDiffStateParams
}

func (f *fakeQ) GetDhcpScopeForPush(_ context.Context, _ uuid.UUID) (dbq.DhcpScopeForPushRow, error) {
	if f.scopeErr != nil {
		return dbq.DhcpScopeForPushRow{}, f.scopeErr
	}
	return f.scope, nil
}
func (f *fakeQ) GetDhcpServerForPush(_ context.Context, _ uuid.UUID) (dbq.DhcpServerForPushRow, error) {
	if f.serverErr != nil {
		return dbq.DhcpServerForPushRow{}, f.serverErr
	}
	return f.server, nil
}
func (f *fakeQ) GetDhcpScopeTemplateForPush(_ context.Context, _ uuid.UUID) (dbq.DhcpScopeTemplate, error) {
	if f.template == nil {
		return dbq.DhcpScopeTemplate{}, pgx.ErrNoRows
	}
	return *f.template, nil
}
func (f *fakeQ) WriteDhcpScopeDiffState(_ context.Context, arg dbq.WriteDhcpScopeDiffStateParams) error {
	cp := arg
	f.diffWriteArgs = &cp
	return nil
}

type fakeKea struct {
	gotSubnet4GetID *int64
	gotSubnet6GetID *int64
	resp            []byte
	rpcErr          error
}

func (f *fakeKea) Subnet4Get(_ context.Context, id int64) ([]byte, error) {
	f.gotSubnet4GetID = &id
	return f.resp, f.rpcErr
}
func (f *fakeKea) Subnet6Get(_ context.Context, id int64) ([]byte, error) {
	f.gotSubnet6GetID = &id
	return f.resp, f.rpcErr
}

func builderReturning(fk *fakeKea) KeaClientBuilder {
	return func(_ dbq.DhcpServerForPushRow) KeaClient { return fk }
}

// ---- fixtures ----

func validScope(t *testing.T, family int32) dbq.DhcpScopeForPushRow {
	t.Helper()
	keaID := int32(1)
	pools := `[{"first":"10.0.0.10","last":"10.0.0.250"}]`
	prefix := "10.0.0.0/24"
	if family == 6 {
		pools = `[{"first":"2001:db8::10","last":"2001:db8::ffff"}]`
		prefix = "2001:db8::/64"
	}
	return dbq.DhcpScopeForPushRow{
		ID:           uuid.New(),
		DhcpServerID: uuid.New(),
		IPFamily:     family,
		Prefix:       prefix,
		PoolsJSON:    json.RawMessage(pools),
		KeaSubnetID:  &keaID,
		Enabled:      true,
	}
}

// matchingKeaResponse builds a subnet{4,6}-get reply that mirrors
// the DCIM render — used by the in_sync test to confirm "DCIM == Kea
// → empty delta → in_sync".
func matchingKeaResponse(scope dbq.DhcpScopeForPushRow) []byte {
	listKey := "subnet4"
	if scope.IPFamily == 6 {
		listKey = "subnet6"
	}
	subnet := map[string]any{
		"id":           int64(*scope.KeaSubnetID),
		"subnet":       scope.Prefix,
		"pools":        renderPoolsForFamily(scope),
		"option-data":  []any{},
		"reservations": []any{},
		// valid-lifetime is bundle.DefaultValidLifetime when scope and
		// template both leave it nil — 3600.
		"valid-lifetime": int64(3600),
	}
	resp := []any{
		map[string]any{
			"result": 0, "text": "ok",
			"arguments": map[string]any{listKey: []any{subnet}},
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

func renderPoolsForFamily(s dbq.DhcpScopeForPushRow) []any {
	if s.IPFamily == 4 {
		return []any{map[string]any{"pool": "10.0.0.10 - 10.0.0.250"}}
	}
	return []any{map[string]any{"pool": "2001:db8::10 - 2001:db8::ffff"}}
}

// ---- DiffScope: pre-call branches ----

func TestDiffScope_ScopeNotFound_ErrorResult(t *testing.T) {
	q := &fakeQ{scopeErr: pgx.ErrNoRows}
	r, err := DiffScope(context.Background(), q, builderReturning(&fakeKea{}), uuid.New())
	if err != nil {
		t.Fatalf("ErrNoRows should not propagate; got %v", err)
	}
	if r.Status != StatusError || r.Error != "scope not found" {
		t.Errorf("result: status=%q error=%q", r.Status, r.Error)
	}
}

func TestDiffScope_NeverPushed_ShortCircuits(t *testing.T) {
	scope := validScope(t, 4)
	scope.KeaSubnetID = nil
	q := &fakeQ{scope: scope}
	fk := &fakeKea{}
	r, _ := DiffScope(context.Background(), q, builderReturning(fk), scope.ID)
	if r.Status != StatusNeverPushed {
		t.Errorf("status: got %q, want never_pushed", r.Status)
	}
	if fk.gotSubnet4GetID != nil {
		t.Errorf("never-pushed scope must NOT trigger Kea call; got %v", fk.gotSubnet4GetID)
	}
}

func TestDiffScope_ServerNotFound_ErrorResult(t *testing.T) {
	scope := validScope(t, 4)
	q := &fakeQ{scope: scope, serverErr: pgx.ErrNoRows}
	r, _ := DiffScope(context.Background(), q, builderReturning(&fakeKea{}), scope.ID)
	if r.Status != StatusError {
		t.Errorf("status: got %q, want error", r.Status)
	}
}

// ---- DiffScope: Kea-side outcomes ----

func TestDiffScope_KeaResult3_MissingFromKea(t *testing.T) {
	scope := validScope(t, 4)
	q := &fakeQ{scope: scope}
	resp, _ := json.Marshal([]any{map[string]any{"result": 3, "text": "not found"}})
	fk := &fakeKea{resp: resp}
	r, _ := DiffScope(context.Background(), q, builderReturning(fk), scope.ID)
	if r.Status != StatusMissingFromKea {
		t.Errorf("status: got %q, want missing_from_kea", r.Status)
	}
	// DCIM render is still populated so the operator sees what DCIM
	// would have shipped.
	if r.DCIMSubnet == nil {
		t.Errorf("DCIMSubnet should be populated even on missing-from-kea")
	}
}

func TestDiffScope_TransportError_ErrorStatusKeepsDCIMRender(t *testing.T) {
	scope := validScope(t, 4)
	q := &fakeQ{scope: scope}
	fk := &fakeKea{rpcErr: errors.New("connection refused")}
	r, _ := DiffScope(context.Background(), q, builderReturning(fk), scope.ID)
	if r.Status != StatusError {
		t.Errorf("status: got %q, want error", r.Status)
	}
	if !strings.Contains(r.Error, "transport_error") {
		t.Errorf("error: got %q", r.Error)
	}
	if r.DCIMSubnet == nil {
		t.Errorf("DCIMSubnet should be populated on transport error")
	}
}

func TestDiffScope_InSync_EmptyDelta(t *testing.T) {
	scope := validScope(t, 4)
	q := &fakeQ{scope: scope}
	fk := &fakeKea{resp: matchingKeaResponse(scope)}
	r, _ := DiffScope(context.Background(), q, builderReturning(fk), scope.ID)
	if r.Status != StatusInSync {
		t.Errorf("status: got %q, want in_sync (delta=%v)", r.Status, r.Delta)
	}
	if len(r.Delta) != 0 {
		t.Errorf("delta should be empty on in_sync; got %v", r.Delta)
	}
}

func TestDiffScope_Drifted_DeltaPopulated(t *testing.T) {
	scope := validScope(t, 4)
	q := &fakeQ{scope: scope}
	// Kea reports a different subnet prefix — drifted.
	resp, _ := json.Marshal([]any{
		map[string]any{
			"result": 0, "text": "ok",
			"arguments": map[string]any{
				"subnet4": []any{map[string]any{
					"id":             int64(1),
					"subnet":         "10.99.99.0/24", // mismatch
					"pools":          renderPoolsForFamily(scope),
					"option-data":    []any{},
					"reservations":   []any{},
					"valid-lifetime": int64(3600),
				}},
			},
		},
	})
	fk := &fakeKea{resp: resp}
	r, _ := DiffScope(context.Background(), q, builderReturning(fk), scope.ID)
	if r.Status != StatusDrifted {
		t.Errorf("status: got %q, want drifted", r.Status)
	}
	subnetDelta, ok := r.Delta["subnet"].(map[string]any)
	if !ok {
		t.Fatalf("delta missing 'subnet' key; got %v", r.Delta)
	}
	if subnetDelta["dcim"] != "10.0.0.0/24" || subnetDelta["kea"] != "10.99.99.0/24" {
		t.Errorf("subnet delta: got %v", subnetDelta)
	}
}

func TestDiffScope_V6_RoutesToSubnet6Get(t *testing.T) {
	scope := validScope(t, 6)
	q := &fakeQ{scope: scope}
	fk := &fakeKea{resp: matchingKeaResponse(scope)}
	_, _ = DiffScope(context.Background(), q, builderReturning(fk), scope.ID)
	if fk.gotSubnet6GetID == nil {
		t.Errorf("v6 scope must call Subnet6Get")
	}
	if fk.gotSubnet4GetID != nil {
		t.Errorf("v6 scope must NOT call Subnet4Get")
	}
}

// ---- pure helpers ----

func TestNormalize_ListsCompareAsMultiset(t *testing.T) {
	a := []any{
		map[string]any{"name": "a", "code": float64(1)},
		map[string]any{"name": "b", "code": float64(2)},
	}
	b := []any{
		map[string]any{"name": "b", "code": float64(2)},
		map[string]any{"name": "a", "code": float64(1)},
	}
	if !equalAsMultiset(a, b) {
		t.Errorf("reordered list with same contents should compare equal as multiset")
	}
}

func TestNormalize_NilAndEmptyListEqual(t *testing.T) {
	if !equalAsMultiset(nil, []any{}) {
		t.Errorf("nil and empty list should compare equal (Kea may omit empty optional)")
	}
}

func TestDiffSubnetObjects_IgnoresKeaAddedKeys(t *testing.T) {
	// Only DCIM-authored keys appear in the delta. Kea added an
	// internal counter; DCIM doesn't author it, so it's ignored.
	dcim := map[string]any{"id": int64(1), "subnet": "10.0.0.0/24"}
	keaResp := map[string]any{
		"id": int64(1), "subnet": "10.0.0.0/24",
		"kea-internal-counter": int64(42),
	}
	delta := DiffSubnetObjects(dcim, keaResp)
	if len(delta) != 0 {
		t.Errorf("Kea-added keys should not produce delta; got %v", delta)
	}
}

func TestDiffSubnetObjects_DCIMKeyMissingFromKea(t *testing.T) {
	dcim := map[string]any{"valid-lifetime": int64(3600)}
	delta := DiffSubnetObjects(dcim, map[string]any{})
	d, ok := delta["valid-lifetime"].(map[string]any)
	if !ok {
		t.Fatalf("delta missing 'valid-lifetime'; got %v", delta)
	}
	if d["dcim"] != int64(3600) || d["kea"] != nil {
		t.Errorf("delta entry: got %v", d)
	}
}

func TestExtractKeaSubnet_Result3MapsToMissing(t *testing.T) {
	raw := []byte(`[{"result":3,"text":"not found"}]`)
	subnet, status, _ := ExtractKeaSubnet(raw, 4)
	if subnet != nil {
		t.Errorf("subnet should be nil when result=3; got %v", subnet)
	}
	if status != StatusMissingFromKea {
		t.Errorf("status: got %q, want missing_from_kea", status)
	}
}

func TestExtractKeaSubnet_MalformedShape(t *testing.T) {
	raw := []byte(`{"not":"a list"}`)
	_, status, errStr := ExtractKeaSubnet(raw, 4)
	if status != StatusError {
		t.Errorf("status: got %q, want error", status)
	}
	if !strings.Contains(errStr, "unexpected Kea response") {
		t.Errorf("error string: got %q", errStr)
	}
}

func TestExtractKeaSubnet_PluckSubnet4(t *testing.T) {
	raw := []byte(`[{"result":0,"arguments":{"subnet4":[{"id":7,"subnet":"10.0.0.0/24"}]}}]`)
	subnet, status, _ := ExtractKeaSubnet(raw, 4)
	if status != "" {
		t.Errorf("status should be empty for a parseable response; got %q", status)
	}
	if subnet["subnet"] != "10.0.0.0/24" {
		t.Errorf("subnet: got %v", subnet)
	}
}

func TestExtractKeaSubnet_PicksRightFamily(t *testing.T) {
	// V6 caller; response has subnet4 instead. Should fail to parse.
	raw := []byte(`[{"result":0,"arguments":{"subnet4":[{"id":1}]}}]`)
	subnet, status, _ := ExtractKeaSubnet(raw, 6)
	if subnet != nil {
		t.Errorf("v6 should not pluck subnet4 entries; got %v", subnet)
	}
	if status != StatusError {
		t.Errorf("status: got %q, want error", status)
	}
}

// ---- PersistDiffState ----

func TestPersistDiffState_DriftedStoresDelta(t *testing.T) {
	q := &fakeQ{}
	id := uuid.New()
	r := Result{
		ScopeID: id, Status: StatusDrifted,
		Delta: map[string]any{"subnet": map[string]any{"dcim": "a", "kea": "b"}},
	}
	if err := PersistDiffState(context.Background(), q, r); err != nil {
		t.Fatalf("PersistDiffState: %v", err)
	}
	if q.diffWriteArgs == nil {
		t.Fatal("WriteDhcpScopeDiffState was not called")
	}
	if q.diffWriteArgs.LastDiffStatus != "drifted" {
		t.Errorf("status: got %q, want drifted", q.diffWriteArgs.LastDiffStatus)
	}
	if len(q.diffWriteArgs.LastDiffDeltaJSON) == 0 {
		t.Errorf("delta_json should be populated on drifted; got empty")
	}
}

func TestPersistDiffState_NonDriftedClearsDelta(t *testing.T) {
	cases := []Status{StatusInSync, StatusNeverPushed, StatusMissingFromKea, StatusError}
	for _, s := range cases {
		q := &fakeQ{}
		_ = PersistDiffState(context.Background(), q, Result{ScopeID: uuid.New(), Status: s})
		if q.diffWriteArgs == nil {
			t.Fatalf("PersistDiffState(%q) didn't write", s)
		}
		if q.diffWriteArgs.LastDiffDeltaJSON != nil {
			t.Errorf("status=%q: delta_json should be cleared; got %s", s, q.diffWriteArgs.LastDiffDeltaJSON)
		}
	}
}

// Compile-time check: *dbq.Queries satisfies diff.Querier.
var _ Querier = (*dbq.Queries)(nil)
