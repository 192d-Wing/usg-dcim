package push

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/kea"
)

// fakeKea is extended in this file with Subnet{4,6}Del capture +
// programmed response so the delete path can be driven end-to-end.
// The push test file's fakeKea is the source of truth for the add/
// update methods; we re-use the same struct and add the delete
// methods here so both tests share the same fake.

func (f *fakeKea) Subnet4Del(_ context.Context, id int64) ([]byte, error) {
	f.gotSubnet4DelID = &id
	return f.subnetResp, f.subnetErr
}

func (f *fakeKea) Subnet6Del(_ context.Context, id int64) ([]byte, error) {
	f.gotSubnet6DelID = &id
	return f.subnetResp, f.subnetErr
}

func TestDeleteScopeFromKea_ScopeNotFound_404Shape(t *testing.T) {
	q := &fakeQ{scopeErr: pgx.ErrNoRows}
	r, err := DeleteScopeFromKea(context.Background(), q, builderReturning(&fakeKea{}), uuid.New())
	if err != nil {
		t.Fatalf("internal error should not propagate for ErrNoRows; got %v", err)
	}
	if r.Status != kea.StatusError || r.Error != "scope not found" {
		t.Errorf("result: got status=%q error=%q", r.Status, r.Error)
	}
}

func TestDeleteScopeFromKea_NeverPushed_ReturnsOKWithoutCallingKea(t *testing.T) {
	// kea_subnet_id is NULL → nothing to clean up. The caller can
	// proceed with the DCIM-side DELETE. Matches Python's early
	// return at services/dhcp_push.py:517-521.
	scope := validScope(t, 4)
	scope.KeaSubnetID = nil
	q := &fakeQ{scope: scope, server: enabledServer()}
	fk := &fakeKea{}
	r, _ := DeleteScopeFromKea(context.Background(), q, builderReturning(fk), scope.ID)
	if r.Status != kea.StatusOK || r.Error != "" {
		t.Errorf("result: got status=%q error=%q, want OK", r.Status, r.Error)
	}
	if fk.gotSubnet4DelID != nil {
		t.Errorf("kea client should NOT be called for never-pushed scope; got DelID=%v", fk.gotSubnet4DelID)
	}
	// No history row for a no-op delete either — Python doesn't
	// record one in this branch.
	if q.historyRow != nil {
		t.Errorf("no-op delete should NOT write history; got %+v", q.historyRow)
	}
}

func TestDeleteScopeFromKea_ServerNotFound_ErrorResult(t *testing.T) {
	scope := validScope(t, 4)
	id := int32(7)
	scope.KeaSubnetID = &id
	q := &fakeQ{scope: scope, serverErr: pgx.ErrNoRows}
	r, _ := DeleteScopeFromKea(context.Background(), q, builderReturning(&fakeKea{}), scope.ID)
	if r.Status != kea.StatusError || !strings.Contains(r.Error, "parent dhcp server") {
		t.Errorf("result: got status=%q error=%q", r.Status, r.Error)
	}
}

func TestDeleteScopeFromKea_ServerDisabled_RefusesWithoutCallingKea(t *testing.T) {
	scope := validScope(t, 4)
	id := int32(7)
	scope.KeaSubnetID = &id
	q := &fakeQ{
		scope:  scope,
		server: dbq.DhcpServerForPushRow{ID: scope.DhcpServerID, KeaURL: "x", Enabled: false},
	}
	fk := &fakeKea{}
	r, _ := DeleteScopeFromKea(context.Background(), q, builderReturning(fk), scope.ID)
	if r.Status != kea.StatusError || !strings.Contains(r.Error, "server disabled") {
		t.Errorf("result: got status=%q error=%q", r.Status, r.Error)
	}
	if fk.gotSubnet4DelID != nil {
		t.Errorf("kea client should NOT be called on disabled server")
	}
}

func TestDeleteScopeFromKea_V4HappyPath(t *testing.T) {
	scope := validScope(t, 4)
	id := int32(7)
	scope.KeaSubnetID = &id
	server := enabledServer()
	server.ID = scope.DhcpServerID
	q := &fakeQ{scope: scope, server: server}
	fk := &fakeKea{subnetResp: []byte(`[{"result":0,"text":"ok"}]`)}

	r, err := DeleteScopeFromKea(context.Background(), q, builderReturning(fk), scope.ID)
	if err != nil {
		t.Fatalf("DeleteScopeFromKea: %v", err)
	}
	if r.Status != kea.StatusOK {
		t.Errorf("status: got %q, want OK", r.Status)
	}
	if fk.gotSubnet4DelID == nil || *fk.gotSubnet4DelID != 7 {
		t.Errorf("Subnet4Del should be called with id=7; got %v", fk.gotSubnet4DelID)
	}
	if fk.gotConfigWrite == nil || fk.gotConfigWrite[0] != "dhcp4" {
		t.Errorf("ConfigWrite should fire for dhcp4; got %v", fk.gotConfigWrite)
	}
	if q.historyRow == nil || q.historyRow.Operation != "delete" || q.historyRow.Status != "ok" {
		t.Errorf("history row: got %+v", q.historyRow)
	}
	// Delete must NOT touch last_push_* or last_diff_*.
	if q.serverLastPushParam != nil {
		t.Errorf("delete should NOT write dhcp_servers.last_push_*; got %+v", q.serverLastPushParam)
	}
	if q.clearedDriftScope != nil {
		t.Errorf("delete should NOT clear last_diff_*; got %v", q.clearedDriftScope)
	}
}

func TestDeleteScopeFromKea_KeaResult3IsOK(t *testing.T) {
	// Kea result=3 = "wasn't there." Semantically "already gone" —
	// the desired post-condition for a delete. InterpretResponse
	// (PR 1) maps this to StatusOK; delete inherits that.
	scope := validScope(t, 4)
	id := int32(7)
	scope.KeaSubnetID = &id
	server := enabledServer()
	server.ID = scope.DhcpServerID
	q := &fakeQ{scope: scope, server: server}
	fk := &fakeKea{subnetResp: []byte(`[{"result":3,"text":"not found"}]`)}

	r, _ := DeleteScopeFromKea(context.Background(), q, builderReturning(fk), scope.ID)
	if r.Status != kea.StatusOK {
		t.Errorf("result=3 should map to OK on delete; got %q", r.Status)
	}
}

func TestDeleteScopeFromKea_V6RoutesToDhcp6Service(t *testing.T) {
	scope := validScope(t, 6)
	id := int32(3)
	scope.KeaSubnetID = &id
	server := enabledServer()
	server.ID = scope.DhcpServerID
	q := &fakeQ{scope: scope, server: server}
	fk := &fakeKea{subnetResp: []byte(`[{"result":0}]`)}

	_, _ = DeleteScopeFromKea(context.Background(), q, builderReturning(fk), scope.ID)
	if fk.gotSubnet6DelID == nil {
		t.Errorf("Subnet6Del should be called for v6 scope")
	}
	if fk.gotConfigWrite == nil || fk.gotConfigWrite[0] != "dhcp6" {
		t.Errorf("ConfigWrite should target dhcp6; got %v", fk.gotConfigWrite)
	}
}

func TestDeleteScopeFromKea_TransportError_RecordsHistory(t *testing.T) {
	scope := validScope(t, 4)
	id := int32(7)
	scope.KeaSubnetID = &id
	server := enabledServer()
	server.ID = scope.DhcpServerID
	q := &fakeQ{scope: scope, server: server}
	fk := &fakeKea{subnetErr: errors.New("connection refused")}

	r, _ := DeleteScopeFromKea(context.Background(), q, builderReturning(fk), scope.ID)
	if r.Status != kea.StatusError {
		t.Errorf("status: got %q, want Error", r.Status)
	}
	if !strings.Contains(r.Error, "transport_error") {
		t.Errorf("error should mention transport_error; got %q", r.Error)
	}
	// History should still be recorded for the failed attempt.
	if q.historyRow == nil || q.historyRow.Operation != "delete" || q.historyRow.Status != "error" {
		t.Errorf("history row missing or wrong: %+v", q.historyRow)
	}
}

func TestDeleteScopeFromKea_ConfigWriteSkippedOnKeaError(t *testing.T) {
	// Same posture as PushScope: a Kea-side error means we don't
	// want to persist whatever half-state Kea is in.
	scope := validScope(t, 4)
	id := int32(7)
	scope.KeaSubnetID = &id
	server := enabledServer()
	server.ID = scope.DhcpServerID
	q := &fakeQ{scope: scope, server: server}
	fk := &fakeKea{subnetResp: []byte(`[{"result":1,"text":"backend wedged"}]`)}

	_, _ = DeleteScopeFromKea(context.Background(), q, builderReturning(fk), scope.ID)
	if fk.gotConfigWrite != nil {
		t.Errorf("ConfigWrite must NOT fire on Kea-side error; got %v", fk.gotConfigWrite)
	}
}
