// Handler tests for the per-scope DHCP push surface (PR 5 of the
// DHCP push port). Covers:
//
//   - POST /ipam/dhcp/scopes/{id}/push
//   - GET  /ipam/dhcp/scopes/{id}/diff
//   - GET  /ipam/dhcp/scopes/{id}/push-history?limit=N
//
// Each test wires a narrow fake of the ipam.Querier that overrides
// only the DHCP push/diff methods, plus a stub kea.Client. The
// orchestrator behaviors themselves are covered exhaustively in
// internal/dhcp/push and internal/dhcp/diff — here we exercise the
// HTTP-layer concerns: 404 routing, ABAC enforcement, audit metadata,
// limit parsing, JSON wire shape parity with Python.
package ipam

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/diff"
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/push"
)

// dhcpPushFakeQ stitches together just the methods the dhcp_push.go
// handlers reach for. Other Querier methods inherit from fakeQ via
// the embedded dhcpPushNoop — they panic-free no-ops because none of
// these endpoints touch them.
type dhcpPushFakeQ struct {
	fakeQ

	scopeFabricID    uuid.UUID
	scopeFabricErr   error
	scopeForPush     dbq.DhcpScopeForPushRow
	scopeForPushErr  error
	serverForPush    dbq.DhcpServerForPushRow
	serverForPushErr error
	template         dbq.DhcpScopeTemplate
	templateErr      error

	historyRows []dbq.DhcpScopePushHistoryRow
	historyLast dbq.ListDhcpScopePushHistoryByScopeParams

	persistedDiff dbq.WriteDhcpScopeDiffStateParams

	allocatedIDs []int32
}

func (f *dhcpPushFakeQ) GetDhcpScopeFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return f.scopeFabricID, f.scopeFabricErr
}

func (f *dhcpPushFakeQ) GetDhcpScopeForPush(_ context.Context, _ uuid.UUID) (dbq.DhcpScopeForPushRow, error) {
	return f.scopeForPush, f.scopeForPushErr
}

func (f *dhcpPushFakeQ) GetDhcpServerForPush(_ context.Context, _ uuid.UUID) (dbq.DhcpServerForPushRow, error) {
	return f.serverForPush, f.serverForPushErr
}

func (f *dhcpPushFakeQ) GetDhcpScopeTemplateForPush(_ context.Context, _ uuid.UUID) (dbq.DhcpScopeTemplate, error) {
	if f.templateErr != nil {
		return dbq.DhcpScopeTemplate{}, f.templateErr
	}
	return f.template, nil
}

func (f *dhcpPushFakeQ) ListKeaSubnetIDsForServer(_ context.Context, _ uuid.UUID) ([]int32, error) {
	return f.allocatedIDs, nil
}

func (f *dhcpPushFakeQ) UpdateDhcpScopeKeaSubnetID(_ context.Context, a dbq.UpdateDhcpScopeKeaSubnetIDParams) error {
	f.scopeForPush.KeaSubnetID = a.KeaSubnetID
	return nil
}

func (f *dhcpPushFakeQ) UpdateDhcpScopeAfterSuccessfulPush(_ context.Context, _ uuid.UUID) error {
	return nil
}

func (f *dhcpPushFakeQ) UpdateDhcpServerLastPush(_ context.Context, _ dbq.UpdateDhcpServerLastPushParams) error {
	return nil
}

func (f *dhcpPushFakeQ) InsertDhcpScopePushHistory(_ context.Context, _ dbq.InsertDhcpScopePushHistoryParams) error {
	return nil
}

func (f *dhcpPushFakeQ) WriteDhcpScopeDiffState(_ context.Context, a dbq.WriteDhcpScopeDiffStateParams) error {
	f.persistedDiff = a
	return nil
}

func (f *dhcpPushFakeQ) ListDhcpScopePushHistoryByScope(_ context.Context, a dbq.ListDhcpScopePushHistoryByScopeParams) ([]dbq.DhcpScopePushHistoryRow, error) {
	f.historyLast = a
	return f.historyRows, nil
}

// fakeKea is a programmable subnetN-{get,add,update,del} stub. Every
// method routes through a shared recorder so the per-family Subnet4
// and Subnet6 variants share one implementation (sonar's S4144
// duplicate-body check otherwise flags the obvious twin methods).
type fakeKea struct {
	add4Resp, add6Resp []byte
	upd4Resp, upd6Resp []byte
	get4Resp, get6Resp []byte
	add4Err, add6Err   error
	upd4Err, upd6Err   error
	get4Err, get6Err   error
	del4Resp, del6Resp []byte
	del4Err, del6Err   error
	writeResp          []byte
	writeErr           error
	addCalls, updCalls int
	delCalls, getCalls int
	writeCalls         int
}

func (k *fakeKea) add(resp []byte, err error) ([]byte, error)   { k.addCalls++; return resp, err }
func (k *fakeKea) upd(resp []byte, err error) ([]byte, error)   { k.updCalls++; return resp, err }
func (k *fakeKea) del(resp []byte, err error) ([]byte, error)   { k.delCalls++; return resp, err }
func (k *fakeKea) get(resp []byte, err error) ([]byte, error)   { k.getCalls++; return resp, err }
func (k *fakeKea) write(resp []byte, err error) ([]byte, error) { k.writeCalls++; return resp, err }

func (k *fakeKea) Subnet4Add(_ context.Context, _ map[string]any) ([]byte, error) {
	return k.add(k.add4Resp, k.add4Err)
}
func (k *fakeKea) Subnet4Update(_ context.Context, _ map[string]any) ([]byte, error) {
	return k.upd(k.upd4Resp, k.upd4Err)
}
func (k *fakeKea) Subnet4Del(_ context.Context, _ int64) ([]byte, error) {
	return k.del(k.del4Resp, k.del4Err)
}
func (k *fakeKea) Subnet4Get(_ context.Context, _ int64) ([]byte, error) {
	return k.get(k.get4Resp, k.get4Err)
}
func (k *fakeKea) Subnet6Add(_ context.Context, _ map[string]any) ([]byte, error) {
	return k.add(k.add6Resp, k.add6Err)
}
func (k *fakeKea) Subnet6Update(_ context.Context, _ map[string]any) ([]byte, error) {
	return k.upd(k.upd6Resp, k.upd6Err)
}
func (k *fakeKea) Subnet6Del(_ context.Context, _ int64) ([]byte, error) {
	return k.del(k.del6Resp, k.del6Err)
}
func (k *fakeKea) Subnet6Get(_ context.Context, _ int64) ([]byte, error) {
	return k.get(k.get6Resp, k.get6Err)
}
func (k *fakeKea) ConfigWrite(_ context.Context, _ []string) ([]byte, error) {
	return k.write(k.writeResp, k.writeErr)
}

// recordingAudit captures InsertAuditLog calls. The audit.Record
// helper builds the InsertAuditLogParams; we keep the raw params so
// assertions can inspect both the action header and the JSON-encoded
// metadata field.
type recordingAudit struct {
	calls []dbq.InsertAuditLogParams
}

func (r *recordingAudit) InsertAuditLog(_ context.Context, p dbq.InsertAuditLogParams) error {
	r.calls = append(r.calls, p)
	return nil
}

func mountDhcpPush(f *dhcpPushFakeQ, k *fakeKea, rec *recordingAudit) http.Handler {
	r := chi.NewRouter()
	(&Handler{
		Q:     f,
		Audit: rec,
		PushKea: func(_ dbq.DhcpServerForPushRow) push.KeaClient { return k },
		DiffKea: func(_ dbq.DhcpServerForPushRow) diff.KeaClient { return k },
	}).Mount(r)
	return r
}

func withPrincipal(req *http.Request, caps ...string) *http.Request {
	return req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{Capabilities: caps}))
}

// ---- POST /push ----

func TestPushDhcpScope_Success(t *testing.T) {
	scopeID := uuid.New()
	serverID := uuid.New()
	fabricID := uuid.New()
	f := &dhcpPushFakeQ{
		scopeFabricID: fabricID,
		scopeForPush: dbq.DhcpScopeForPushRow{
			ID:           scopeID,
			DhcpServerID: serverID,
			IPFamily:     4,
			Prefix:       "10.0.0.0/24",
			PoolsJSON:    json.RawMessage(`[]`),
			PdPoolsJSON:  json.RawMessage(`[]`),
			OptionsJSON:  json.RawMessage(`[]`),
			ReservationsJSON: json.RawMessage(`[]`),
			Enabled:      true,
		},
		serverForPush: dbq.DhcpServerForPushRow{
			ID: serverID, KeaURL: "http://kea.example", Enabled: true,
		},
	}
	k := &fakeKea{
		add4Resp: []byte(`[{"result":0,"text":"ok"}]`),
	}
	rec := &recordingAudit{}
	req := httptest.NewRequest("POST", "/ipam/dhcp/scopes/"+scopeID.String()+"/push", nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountDhcpPush(f, k, rec).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	// Raw body assertion before JSON decoding catches the subtle
	// nilIfEmpty contract: empty Result.Error must emit JSON `null`,
	// not `""`. After decode into a *string field, both look like
	// nil — the regression guard has to inspect the bytes directly.
	if !bytes.Contains(w.Body.Bytes(), []byte(`"error":null`)) {
		t.Errorf("body must contain \"error\":null, got %s", w.Body.String())
	}
	var got pushResultBody
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ScopeID != scopeID.String() || got.Status != "ok" || got.Error != nil {
		t.Errorf("body = %+v", got)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("audit events = %d, want 1", len(rec.calls))
	}
	e := rec.calls[0]
	if e.Action != "dhcp_scope.push" {
		t.Errorf("action = %q", e.Action)
	}
	if e.TargetType == nil || *e.TargetType != "dhcp_scope" {
		t.Errorf("target_type = %v", e.TargetType)
	}
	if e.TargetID == nil || *e.TargetID != scopeID.String() {
		t.Errorf("target_id = %v", e.TargetID)
	}
	var meta map[string]any
	if err := json.Unmarshal(e.MetadataJson, &meta); err != nil {
		t.Fatalf("metadata decode: %v (raw=%s)", err, e.MetadataJson)
	}
	if meta["dhcp_server_id"] != serverID.String() {
		t.Errorf("metadata dhcp_server_id = %v", meta["dhcp_server_id"])
	}
	if meta["status"] != "ok" {
		t.Errorf("metadata status = %v", meta["status"])
	}
}

func TestPushDhcpScope_ScopeNotFound(t *testing.T) {
	f := &dhcpPushFakeQ{scopeFabricErr: pgx.ErrNoRows}
	rec := &recordingAudit{}
	req := httptest.NewRequest("POST", "/ipam/dhcp/scopes/"+uuid.New().String()+"/push", nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountDhcpPush(f, &fakeKea{}, rec).ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d", w.Code)
	}
}

func TestPushDhcpScope_ForbiddenWithoutScopedCap(t *testing.T) {
	// PR 54 — a principal whose fabric scope doesn't include the
	// scope's fabric must get 403, not 200.
	scopeID := uuid.New()
	fabricID := uuid.New()
	f := &dhcpPushFakeQ{scopeFabricID: fabricID}
	rec := &recordingAudit{}
	req := httptest.NewRequest("POST", "/ipam/dhcp/scopes/"+scopeID.String()+"/push", nil)
	otherFabric := uuid.New()
	p := auth.Principal{
		Capabilities: []string{"ipam:dhcp-scopes:push"},
		Scopes: map[string]auth.Scope{
			"ipam:dhcp-scopes:push": {FabricIDs: map[uuid.UUID]struct{}{otherFabric: {}}},
		},
	}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	w := httptest.NewRecorder()
	mountDhcpPush(f, &fakeKea{}, rec).ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
	if len(rec.calls) != 0 {
		t.Errorf("audit fired on forbidden request: %+v", rec.calls)
	}
}

func TestPushDhcpScope_BadID(t *testing.T) {
	rec := &recordingAudit{}
	req := httptest.NewRequest("POST", "/ipam/dhcp/scopes/not-a-uuid/push", nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountDhcpPush(&dhcpPushFakeQ{}, &fakeKea{}, rec).ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d", w.Code)
	}
}

// ---- GET /diff ----

func TestDiffDhcpScope_NeverPushed(t *testing.T) {
	scopeID := uuid.New()
	serverID := uuid.New()
	fabricID := uuid.New()
	f := &dhcpPushFakeQ{
		scopeFabricID: fabricID,
		scopeForPush: dbq.DhcpScopeForPushRow{
			ID: scopeID, DhcpServerID: serverID, IPFamily: 4, Prefix: "10.0.0.0/24",
			PoolsJSON: json.RawMessage(`[]`), PdPoolsJSON: json.RawMessage(`[]`),
			OptionsJSON: json.RawMessage(`[]`), ReservationsJSON: json.RawMessage(`[]`),
			Enabled: true,
			// KeaSubnetID nil → never_pushed short-circuit
		},
	}
	req := httptest.NewRequest("GET", "/ipam/dhcp/scopes/"+scopeID.String()+"/diff", nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountDhcpPush(f, &fakeKea{}, &recordingAudit{}).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	rawBody := w.Body.Bytes()
	var got diffResultBody
	if err := json.Unmarshal(rawBody, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "never_pushed" {
		t.Errorf("status = %q, want never_pushed", got.Status)
	}
	if string(f.persistedDiff.LastDiffStatus) != "never_pushed" {
		t.Errorf("persisted status = %q", f.persistedDiff.LastDiffStatus)
	}
	if f.persistedDiff.LastDiffDeltaJSON != nil {
		t.Errorf("never_pushed should clear delta_json, got %s", string(f.persistedDiff.LastDiffDeltaJSON))
	}
	// Python parity (services/dhcp_push.py:865): every non-drifted
	// DiffResult passes delta={} explicitly, so the wire shape carries
	// "delta":{} rather than "delta":null. Without this guard the Go
	// side would silently regress to null (nil map JSON-encodes to
	// null without omitempty).
	if !bytes.Contains(rawBody, []byte(`"delta":{}`)) {
		t.Errorf("body must contain \"delta\":{} for never_pushed, got %s", rawBody)
	}
	if bytes.Contains(rawBody, []byte(`"delta":null`)) {
		t.Errorf("never_pushed must NOT emit \"delta\":null, got %s", rawBody)
	}
}

func TestDiffDhcpScope_ForbiddenWithoutReadCap(t *testing.T) {
	scopeID := uuid.New()
	fabricID := uuid.New()
	f := &dhcpPushFakeQ{scopeFabricID: fabricID}
	req := httptest.NewRequest("GET", "/ipam/dhcp/scopes/"+scopeID.String()+"/diff", nil)
	p := auth.Principal{
		Capabilities: []string{"ipam:dhcp-scopes:read"},
		Scopes: map[string]auth.Scope{
			"ipam:dhcp-scopes:read": {FabricIDs: map[uuid.UUID]struct{}{uuid.New(): {}}},
		},
	}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	w := httptest.NewRecorder()
	mountDhcpPush(f, &fakeKea{}, &recordingAudit{}).ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

// ---- GET /push-history ----

func TestPushHistory_Success(t *testing.T) {
	scopeID := uuid.New()
	serverID := uuid.New()
	keaID := int32(5)
	errStr := "boom"
	dur := int32(123)
	when := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	f := &dhcpPushFakeQ{
		scopeFabricID: uuid.New(),
		historyRows: []dbq.DhcpScopePushHistoryRow{
			{
				ID: uuid.New(), ScopeID: scopeID, ServerID: serverID,
				Operation: "add", KeaSubnetID: &keaID,
				Status: "ok", Error: nil, DurationMS: &dur, AttemptedAt: when,
			},
			{
				ID: uuid.New(), ScopeID: scopeID, ServerID: serverID,
				Operation: "update", KeaSubnetID: &keaID,
				Status: "error", Error: &errStr, DurationMS: &dur, AttemptedAt: when,
			},
		},
	}
	req := httptest.NewRequest("GET", "/ipam/dhcp/scopes/"+scopeID.String()+"/push-history?limit=25", nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountDhcpPush(f, &fakeKea{}, &recordingAudit{}).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if f.historyLast.Limit != 25 {
		t.Errorf("limit = %d, want 25", f.historyLast.Limit)
	}
	rawBody := w.Body.Bytes()
	var got pushHistoryBody
	if err := json.Unmarshal(rawBody, &got); err != nil {
		t.Fatal(err)
	}
	if got.ScopeID != scopeID.String() {
		t.Errorf("scope_id = %q", got.ScopeID)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(got.Entries))
	}
	if got.Entries[0].Operation != "add" || got.Entries[0].Status != "ok" || got.Entries[0].Error != nil {
		t.Errorf("entry[0] = %+v", got.Entries[0])
	}
	if got.Entries[1].Error == nil || *got.Entries[1].Error != "boom" {
		t.Errorf("entry[1] error = %v", got.Entries[1].Error)
	}
	// Python parity: tz-aware DateTime(timezone=True) column rendered
	// via isoformat() produces 6 microseconds digits + signed offset.
	// UTC must emit "+00:00", not the literal "Z" form (Z is what
	// Go's Z07:00 format gives — wrong for parity).
	want := "2026-06-01T12:00:00.000000+00:00"
	if got.Entries[0].AttemptedAt != want {
		t.Errorf("attempted_at = %q, want %q", got.Entries[0].AttemptedAt, want)
	}
	// First entry's error is nil → wire shape must show null, not "".
	if !bytes.Contains(rawBody, []byte(`"error":null`)) {
		t.Errorf("body must contain \"error\":null for the ok entry, got %s", rawBody)
	}
}

func TestPushHistory_DefaultLimit(t *testing.T) {
	scopeID := uuid.New()
	f := &dhcpPushFakeQ{scopeFabricID: uuid.New()}
	req := httptest.NewRequest("GET", "/ipam/dhcp/scopes/"+scopeID.String()+"/push-history", nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountDhcpPush(f, &fakeKea{}, &recordingAudit{}).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if f.historyLast.Limit != 50 {
		t.Errorf("default limit = %d, want 50", f.historyLast.Limit)
	}
}

func TestPushHistory_LimitClamped(t *testing.T) {
	scopeID := uuid.New()
	f := &dhcpPushFakeQ{scopeFabricID: uuid.New()}
	req := httptest.NewRequest("GET", "/ipam/dhcp/scopes/"+scopeID.String()+"/push-history?limit=9999", nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountDhcpPush(f, &fakeKea{}, &recordingAudit{}).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if f.historyLast.Limit != 500 {
		t.Errorf("limit = %d, want clamped to 500", f.historyLast.Limit)
	}
}

func TestPushHistory_NotFound(t *testing.T) {
	f := &dhcpPushFakeQ{scopeFabricErr: pgx.ErrNoRows}
	req := httptest.NewRequest("GET", "/ipam/dhcp/scopes/"+uuid.New().String()+"/push-history", nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountDhcpPush(f, &fakeKea{}, &recordingAudit{}).ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
