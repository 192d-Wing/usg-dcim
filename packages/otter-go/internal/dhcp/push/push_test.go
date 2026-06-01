package push

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/kea"
)

// fakeQ is the slimmest DB stub the orchestrator can drive against.
// Seed scope/server/template before the test; capture every write
// the orchestrator makes so the test can assert on side effects
// (rollback, status writes, history rows).
type fakeQ struct {
	scope            dbq.DhcpScopeForPushRow
	scopeErr         error
	server           dbq.DhcpServerForPushRow
	serverErr        error
	template         *dbq.DhcpScopeTemplate
	claimedKeaIDs    []int32

	// captures
	scopeKeaIDWrites    []*int32 // each call to UpdateDhcpScopeKeaSubnetID
	serverLastPushParam *dbq.UpdateDhcpServerLastPushParams
	historyRow          *dbq.InsertDhcpScopePushHistoryParams
	clearedDriftScope   *uuid.UUID
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
func (f *fakeQ) ListKeaSubnetIDsForServer(_ context.Context, _ uuid.UUID) ([]int32, error) {
	return append([]int32(nil), f.claimedKeaIDs...), nil
}
func (f *fakeQ) UpdateDhcpScopeKeaSubnetID(_ context.Context, arg dbq.UpdateDhcpScopeKeaSubnetIDParams) error {
	f.scopeKeaIDWrites = append(f.scopeKeaIDWrites, arg.KeaSubnetID)
	return nil
}
func (f *fakeQ) UpdateDhcpScopeAfterSuccessfulPush(_ context.Context, id uuid.UUID) error {
	f.clearedDriftScope = &id
	return nil
}
func (f *fakeQ) UpdateDhcpServerLastPush(_ context.Context, arg dbq.UpdateDhcpServerLastPushParams) error {
	cp := arg
	f.serverLastPushParam = &cp
	return nil
}
func (f *fakeQ) InsertDhcpScopePushHistory(_ context.Context, arg dbq.InsertDhcpScopePushHistoryParams) error {
	cp := arg
	f.historyRow = &cp
	return nil
}

// fakeKea captures the inbound subnet object and replays a
// programmed response so the orchestrator's response-handling
// branches are pin-down-able. Delete methods (Subnet4Del/Subnet6Del)
// are defined in delete_test.go to keep the test-file structure
// matching the production-file split.
type fakeKea struct {
	gotSubnet4Add    map[string]any
	gotSubnet4Update map[string]any
	gotSubnet4DelID  *int64
	gotSubnet6Add    map[string]any
	gotSubnet6Update map[string]any
	gotSubnet6DelID  *int64
	gotConfigWrite   []string

	subnetResp     []byte
	subnetErr      error
	configRespCode int
}

func (f *fakeKea) Subnet4Add(_ context.Context, s map[string]any) ([]byte, error) {
	f.gotSubnet4Add = s
	return f.subnetResp, f.subnetErr
}
func (f *fakeKea) Subnet4Update(_ context.Context, s map[string]any) ([]byte, error) {
	f.gotSubnet4Update = s
	return f.subnetResp, f.subnetErr
}
func (f *fakeKea) Subnet6Add(_ context.Context, s map[string]any) ([]byte, error) {
	f.gotSubnet6Add = s
	return f.subnetResp, f.subnetErr
}
func (f *fakeKea) Subnet6Update(_ context.Context, s map[string]any) ([]byte, error) {
	f.gotSubnet6Update = s
	return f.subnetResp, f.subnetErr
}
func (f *fakeKea) ConfigWrite(_ context.Context, services []string) ([]byte, error) {
	f.gotConfigWrite = services
	return []byte(`[{"result":0}]`), nil
}

func builderReturning(fk *fakeKea) KeaClientBuilder {
	return func(_ dbq.DhcpServerForPushRow) KeaClient { return fk }
}

// ---- fixtures ----

func validScope(t *testing.T, family int32) dbq.DhcpScopeForPushRow {
	t.Helper()
	return dbq.DhcpScopeForPushRow{
		ID:           uuid.New(),
		DhcpServerID: uuid.New(),
		IPFamily:     family,
		Prefix:       prefixFor(family),
		PoolsJSON:    json.RawMessage(poolsFor(family)),
		Enabled:      true,
	}
}

func prefixFor(family int32) string {
	if family == 4 {
		return "10.0.0.0/24"
	}
	return "2001:db8::/64"
}

func poolsFor(family int32) string {
	if family == 4 {
		return `[{"first":"10.0.0.10","last":"10.0.0.250"}]`
	}
	return `[{"first":"2001:db8::10","last":"2001:db8::ffff"}]`
}

func enabledServer() dbq.DhcpServerForPushRow {
	return dbq.DhcpServerForPushRow{
		ID: uuid.New(), KeaURL: "https://kea.example", Enabled: true,
	}
}

// ---- AllocateKeaSubnetID ----

func TestAllocateKeaSubnetID_PicksOneWhenEmpty(t *testing.T) {
	q := &fakeQ{}
	got, err := AllocateKeaSubnetID(context.Background(), q, uuid.New())
	if err != nil {
		t.Fatalf("AllocateKeaSubnetID: %v", err)
	}
	if got != 1 {
		t.Errorf("first allocation should be 1, got %d", got)
	}
}

func TestAllocateKeaSubnetID_FillsLowestFreeSlot(t *testing.T) {
	q := &fakeQ{claimedKeaIDs: []int32{1, 3, 7}}
	got, _ := AllocateKeaSubnetID(context.Background(), q, uuid.New())
	if got != 2 {
		t.Errorf("should pick lowest free (2), got %d", got)
	}
}

func TestAllocateKeaSubnetID_HandlesDenseSet(t *testing.T) {
	q := &fakeQ{claimedKeaIDs: []int32{1, 2, 3, 4, 5}}
	got, _ := AllocateKeaSubnetID(context.Background(), q, uuid.New())
	if got != 6 {
		t.Errorf("with dense {1..5}, should pick 6; got %d", got)
	}
}

// ---- PushScope: pre-call refusals ----

func TestPushScope_ScopeNotFound_404Shape(t *testing.T) {
	q := &fakeQ{scopeErr: pgx.ErrNoRows}
	r, err := PushScope(context.Background(), q, builderReturning(&fakeKea{}), uuid.New())
	if err != nil {
		t.Fatalf("internal error should not propagate for ErrNoRows; got %v", err)
	}
	if r.Status != kea.StatusError || r.Error != "scope not found" {
		t.Errorf("result: got status=%q error=%q", r.Status, r.Error)
	}
}

func TestPushScope_ServerNotFound_ErrorResult(t *testing.T) {
	q := &fakeQ{
		scope:     validScope(t, 4),
		serverErr: pgx.ErrNoRows,
	}
	r, _ := PushScope(context.Background(), q, builderReturning(&fakeKea{}), uuid.New())
	if r.Status != kea.StatusError || !strings.Contains(r.Error, "parent dhcp server") {
		t.Errorf("result: got status=%q error=%q", r.Status, r.Error)
	}
}

func TestPushScope_ServerDisabled_RefusesWithoutCallingKea(t *testing.T) {
	scope := validScope(t, 4)
	q := &fakeQ{
		scope:  scope,
		server: dbq.DhcpServerForPushRow{ID: scope.DhcpServerID, KeaURL: "x", Enabled: false},
	}
	fk := &fakeKea{}
	r, _ := PushScope(context.Background(), q, builderReturning(fk), scope.ID)
	if r.Status != kea.StatusError || !strings.Contains(r.Error, "server disabled") {
		t.Errorf("result: got status=%q error=%q", r.Status, r.Error)
	}
	if fk.gotSubnet4Add != nil || fk.gotSubnet4Update != nil {
		t.Errorf("kea client should NOT be called for disabled server; got Subnet4Add=%v", fk.gotSubnet4Add)
	}
}

// ---- PushScope: happy path ----

func TestPushScope_V4FirstPush_AllocatesIDAndCallsAddThenConfigWrite(t *testing.T) {
	scope := validScope(t, 4)
	server := enabledServer()
	server.ID = scope.DhcpServerID
	q := &fakeQ{scope: scope, server: server, claimedKeaIDs: []int32{1, 3}} // 2 is lowest free
	fk := &fakeKea{subnetResp: []byte(`[{"result":0,"text":"ok"}]`)}

	r, err := PushScope(context.Background(), q, builderReturning(fk), scope.ID)
	if err != nil {
		t.Fatalf("PushScope: %v", err)
	}
	if r.Status != kea.StatusOK || r.Error != "" {
		t.Errorf("result: got status=%q error=%q, want OK", r.Status, r.Error)
	}
	if r.KeaSubnetID == nil || *r.KeaSubnetID != 2 {
		t.Errorf("Result.KeaSubnetID: got %v, want 2 (lowest free)", r.KeaSubnetID)
	}
	if fk.gotSubnet4Add == nil {
		t.Fatalf("Subnet4Add should have been called")
	}
	if id, _ := fk.gotSubnet4Add["id"].(int64); id != 2 {
		t.Errorf("Kea subnet id: got %v, want 2", fk.gotSubnet4Add["id"])
	}
	if fk.gotConfigWrite == nil || fk.gotConfigWrite[0] != "dhcp4" {
		t.Errorf("ConfigWrite should fire for [dhcp4]; got %v", fk.gotConfigWrite)
	}
	if q.serverLastPushParam == nil || q.serverLastPushParam.LastPushStatus != "ok" {
		t.Errorf("server last_push not written as ok; got %+v", q.serverLastPushParam)
	}
	if q.historyRow == nil || q.historyRow.Status != "ok" || q.historyRow.Operation != "add" {
		t.Errorf("history row: got %+v", q.historyRow)
	}
	if q.clearedDriftScope == nil || *q.clearedDriftScope != scope.ID {
		t.Errorf("successful push should clear last_diff_*; got %v", q.clearedDriftScope)
	}
}

func TestPushScope_V4Update_UsesSubnet4UpdateNotAdd(t *testing.T) {
	scope := validScope(t, 4)
	existingID := int32(5)
	scope.KeaSubnetID = &existingID
	server := enabledServer()
	server.ID = scope.DhcpServerID
	q := &fakeQ{scope: scope, server: server}
	fk := &fakeKea{subnetResp: []byte(`[{"result":0}]`)}

	r, _ := PushScope(context.Background(), q, builderReturning(fk), scope.ID)
	if r.Status != kea.StatusOK {
		t.Errorf("status: got %q, want OK", r.Status)
	}
	if fk.gotSubnet4Add != nil {
		t.Errorf("Update should NOT call Subnet4Add; got %v", fk.gotSubnet4Add)
	}
	if fk.gotSubnet4Update == nil {
		t.Errorf("Subnet4Update should have been called")
	}
	if q.historyRow == nil || q.historyRow.Operation != "update" {
		t.Errorf("history operation: got %v, want update", q.historyRow)
	}
}

func TestPushScope_V6FirstPush_RoutesToDhcp6Service(t *testing.T) {
	scope := validScope(t, 6)
	server := enabledServer()
	server.ID = scope.DhcpServerID
	q := &fakeQ{scope: scope, server: server}
	fk := &fakeKea{subnetResp: []byte(`[{"result":0}]`)}

	_, _ = PushScope(context.Background(), q, builderReturning(fk), scope.ID)
	if fk.gotSubnet6Add == nil {
		t.Errorf("Subnet6Add should have been called for v6 scope")
	}
	if fk.gotConfigWrite == nil || fk.gotConfigWrite[0] != "dhcp6" {
		t.Errorf("ConfigWrite should target dhcp6; got %v", fk.gotConfigWrite)
	}
}

// ---- PushScope: failure paths + rollback ----

func TestPushScope_TransportError_RollsBackOptimisticIDClaim(t *testing.T) {
	// First-push fails transport → kea_subnet_id reverted to NULL.
	scope := validScope(t, 4)
	server := enabledServer()
	server.ID = scope.DhcpServerID
	q := &fakeQ{scope: scope, server: server}
	fk := &fakeKea{subnetErr: errors.New("connection refused")}

	r, _ := PushScope(context.Background(), q, builderReturning(fk), scope.ID)
	if r.Status != kea.StatusError {
		t.Errorf("status: got %q, want Error", r.Status)
	}
	if !strings.Contains(r.Error, "transport_error") {
		t.Errorf("error string should mention transport_error; got %q", r.Error)
	}
	if r.KeaSubnetID != nil {
		t.Errorf("first-push transport failure should roll back kea_subnet_id to nil; got %v", r.KeaSubnetID)
	}
	// Two write calls: the claim, then the rollback to nil.
	if len(q.scopeKeaIDWrites) != 2 {
		t.Fatalf("expected 2 kea_subnet_id writes (claim + rollback); got %d", len(q.scopeKeaIDWrites))
	}
	if q.scopeKeaIDWrites[0] == nil || q.scopeKeaIDWrites[1] != nil {
		t.Errorf("write order should be (claim ptr, rollback nil); got %v %v",
			q.scopeKeaIDWrites[0], q.scopeKeaIDWrites[1])
	}
	if q.clearedDriftScope != nil {
		t.Errorf("error path must NOT clear last_diff_*; got %v", q.clearedDriftScope)
	}
}

func TestPushScope_UpdateTransportError_DoesNotRollbackKeaSubnetID(t *testing.T) {
	// An update that fails leaves kea_subnet_id alone — the id was
	// claimed in a prior successful push and is still valid in Kea.
	scope := validScope(t, 4)
	existingID := int32(7)
	scope.KeaSubnetID = &existingID
	server := enabledServer()
	server.ID = scope.DhcpServerID
	q := &fakeQ{scope: scope, server: server}
	fk := &fakeKea{subnetErr: errors.New("connection refused")}

	r, _ := PushScope(context.Background(), q, builderReturning(fk), scope.ID)
	if r.KeaSubnetID == nil || *r.KeaSubnetID != 7 {
		t.Errorf("update failure must NOT roll back kea_subnet_id; got %v", r.KeaSubnetID)
	}
	if len(q.scopeKeaIDWrites) != 0 {
		t.Errorf("update path must not write kea_subnet_id; got %d writes", len(q.scopeKeaIDWrites))
	}
}

func TestPushScope_KeaSidesError_ResultStatusFromInterpret(t *testing.T) {
	scope := validScope(t, 4)
	server := enabledServer()
	server.ID = scope.DhcpServerID
	q := &fakeQ{scope: scope, server: server}
	fk := &fakeKea{subnetResp: []byte(`[{"result":1,"text":"subnet id already exists"}]`)}

	r, _ := PushScope(context.Background(), q, builderReturning(fk), scope.ID)
	if r.Status != kea.StatusError {
		t.Errorf("status: got %q, want Error", r.Status)
	}
	if !strings.Contains(r.Error, "already exists") {
		t.Errorf("Result.Error should carry Kea text; got %q", r.Error)
	}
	if q.serverLastPushParam == nil || q.serverLastPushParam.LastPushStatus != "error" {
		t.Errorf("server last_push should be 'error'; got %+v", q.serverLastPushParam)
	}
	// On Kea-side error, config_write must NOT have fired (would
	// persist a half-broken config).
	if fk.gotConfigWrite != nil {
		t.Errorf("ConfigWrite must NOT fire on Kea-side error; got %v", fk.gotConfigWrite)
	}
}

func TestPushScope_HookMissing_StatusUnsupported(t *testing.T) {
	scope := validScope(t, 4)
	server := enabledServer()
	server.ID = scope.DhcpServerID
	q := &fakeQ{scope: scope, server: server}
	fk := &fakeKea{subnetResp: []byte(`[{"result":2,"text":"command not supported"}]`)}

	r, _ := PushScope(context.Background(), q, builderReturning(fk), scope.ID)
	if r.Status != kea.StatusUnsupported {
		t.Errorf("status: got %q, want Unsupported", r.Status)
	}
}

// ---- Template + Renderer integration ----

func TestPushScope_TemplateMergedIntoSubnetPayload(t *testing.T) {
	scope := validScope(t, 4)
	tplID := uuid.New()
	scope.TemplateID = &tplID
	server := enabledServer()
	server.ID = scope.DhcpServerID
	tpl := dbq.DhcpScopeTemplate{
		ID:          tplID,
		IPFamily:    4,
		OptionsJSON: json.RawMessage(`[{"code":3,"name":"routers","data":"10.0.0.1"}]`),
	}
	q := &fakeQ{scope: scope, server: server, template: &tpl}
	fk := &fakeKea{subnetResp: []byte(`[{"result":0}]`)}

	_, _ = PushScope(context.Background(), q, builderReturning(fk), scope.ID)

	opts, _ := fk.gotSubnet4Add["option-data"].([]map[string]any)
	if len(opts) != 1 || opts[0]["name"] != "routers" {
		t.Errorf("template options should land in subnet payload; got %v", opts)
	}
}

func TestPushScope_MissingTemplate_RendersScopeOnly(t *testing.T) {
	// Scope references a template that was deleted. Python's merge
	// fallback (None) renders with scope-only values; Go must match.
	scope := validScope(t, 4)
	tplID := uuid.New()
	scope.TemplateID = &tplID
	server := enabledServer()
	server.ID = scope.DhcpServerID
	q := &fakeQ{scope: scope, server: server, template: nil} // ErrNoRows
	fk := &fakeKea{subnetResp: []byte(`[{"result":0}]`)}

	r, _ := PushScope(context.Background(), q, builderReturning(fk), scope.ID)
	if r.Status != kea.StatusOK {
		t.Errorf("missing template should not block push; got %q error=%q", r.Status, r.Error)
	}
	if fk.gotSubnet4Add == nil {
		t.Errorf("Subnet4Add should have been called")
	}
}

// ---- DefaultKeaClientBuilder ----

func TestDefaultKeaClientBuilder_PicksUpAuthCredentials(t *testing.T) {
	user := "monitor"
	pass := "s3cr3t"
	server := dbq.DhcpServerForPushRow{
		ID: uuid.New(), KeaURL: "https://kea.example",
		AuthUsername: &user, AuthPassword: &pass,
	}
	c := DefaultKeaClientBuilder(server)
	if c == nil {
		t.Fatal("builder returned nil")
	}
	// We can't peek inside the kea.Client to verify credentials
	// directly, but we can confirm the builder doesn't panic on
	// nil pointers + returns a usable client.
}

func TestDefaultKeaClientBuilder_NilAuthPointersAreSafe(t *testing.T) {
	server := dbq.DhcpServerForPushRow{
		ID: uuid.New(), KeaURL: "https://kea.example",
		AuthUsername: nil, AuthPassword: nil,
	}
	c := DefaultKeaClientBuilder(server)
	if c == nil {
		t.Fatal("builder returned nil with nil credentials")
	}
}

// ---- Smoke: ensure Status type doesn't drift ----

func TestStatusLiteralsMatchPythonAndKeaPkg(t *testing.T) {
	if string(kea.StatusOK) != "ok" {
		t.Errorf("StatusOK literal must be 'ok' for audit log parity; got %q", kea.StatusOK)
	}
	if string(kea.StatusError) != "error" {
		t.Errorf("StatusError literal must be 'error'; got %q", kea.StatusError)
	}
	if string(kea.StatusUnsupported) != "unsupported" {
		t.Errorf("StatusUnsupported literal must be 'unsupported'; got %q", kea.StatusUnsupported)
	}
}

// Compile-time check: *dbq.Queries satisfies push.Querier.
var _ Querier = (*dbq.Queries)(nil)

// Avoid http import warning when http is referenced only in fake setup helpers.
var _ = http.StatusOK
