// Handler tests for the bulk DHCP push surface (PR 6 of the DHCP
// push port). Per-orchestrator behavior is covered exhaustively in
// internal/dhcp/push and internal/dhcp/diff bulk tests; this file
// pin-downs the HTTP shape concerns:
//
//   - 404 when the server doesn't exist
//   - 403 when the principal's scope doesn't include the server's fabric
//   - 200 + a results array on success (per-scope failures don't 500)
//   - Wire-shape parity with Python: server_id / total / counts / results
//   - Audit metadata carries total + counts on push paths (push-all,
//     push-drifted); diff-all does NOT emit an audit row (matches
//     Python at line 2602).
package ipam

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/diff"
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/push"
)

// dhcpBulkFakeQ overrides only the bulk-specific Querier methods.
// Per-scope DHCP methods inherit fakeQ's dhcpPushNoop embed, which
// returns pgx.ErrNoRows — that turns each per-scope PushScope /
// DiffScope into a kea.StatusError result without ever touching the
// Kea client. Enough to exercise the HTTP layer + the loop's
// aggregation logic.
type dhcpBulkFakeQ struct {
	fakeQ

	serverFabricID  uuid.UUID
	serverFabricErr error

	enabled    []uuid.UUID
	drifted    []uuid.UUID
	allWithPri []dbq.ListAllScopeIDsAndPriorDriftForServerRow
}

func (f *dhcpBulkFakeQ) GetDhcpServerFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return f.serverFabricID, f.serverFabricErr
}
func (f *dhcpBulkFakeQ) ListEnabledScopeIDsForServer(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return f.enabled, nil
}
func (f *dhcpBulkFakeQ) ListDriftedScopeIDsForServer(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return f.drifted, nil
}
func (f *dhcpBulkFakeQ) ListAllScopeIDsAndPriorDriftForServer(_ context.Context, _ uuid.UUID) ([]dbq.ListAllScopeIDsAndPriorDriftForServerRow, error) {
	return f.allWithPri, nil
}

func mountDhcpBulk(f *dhcpBulkFakeQ, rec *recordingAudit) http.Handler {
	r := chi.NewRouter()
	(&Handler{
		Q:     f,
		Audit: rec,
		PushKea: func(_ dbq.GetDhcpServerForPushRow) push.KeaClient { return &fakeKea{} },
		DiffKea: func(_ dbq.GetDhcpServerForPushRow) diff.KeaClient { return &fakeKea{} },
	}).Mount(r)
	return r
}

// runBulkRequest is the test boilerplate every bulk-endpoint test
// shares: stand up the fake + recorder + router, build the request
// with the wildcard principal, serve it, fail the test on a non-
// 200 status. Returns the response recorder so each test asserts
// on the body shape it cares about.
func runBulkRequest(t *testing.T, method, path string, f *dhcpBulkFakeQ, rec *recordingAudit) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountDhcpBulk(f, rec).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	return w
}

// ---- push-all ----

func TestPushAllDhcpScopes_EmptyServer_200WithZeroCounts(t *testing.T) {
	serverID := uuid.New()
	f := &dhcpBulkFakeQ{serverFabricID: uuid.New()}
	rec := &recordingAudit{}
	w := runBulkRequest(t, "POST", "/ipam/dhcp/servers/"+serverID.String()+"/scopes/push-all", f, rec)
	var got bulkPushBody
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ServerID != serverID.String() || got.Total != 0 {
		t.Errorf("body = %+v", got)
	}
	if got.Counts["ok"] != 0 || got.Counts["error"] != 0 || got.Counts["unsupported"] != 0 {
		t.Errorf("counts = %+v, want all-zero", got.Counts)
	}
	// Audit fires once with the aggregate, not per-scope.
	if len(rec.calls) != 1 {
		t.Fatalf("audit calls = %d, want 1", len(rec.calls))
	}
	if rec.calls[0].Action != "dhcp_scope.push_all" {
		t.Errorf("action = %q", rec.calls[0].Action)
	}
}

func TestPushAllDhcpScopes_ServerNotFound_404(t *testing.T) {
	f := &dhcpBulkFakeQ{serverFabricErr: pgx.ErrNoRows}
	req := httptest.NewRequest("POST", "/ipam/dhcp/servers/"+uuid.New().String()+"/scopes/push-all", nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountDhcpBulk(f, &recordingAudit{}).ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestPushAllDhcpScopes_ForbiddenWithoutScopedCap(t *testing.T) {
	serverID := uuid.New()
	fabricID := uuid.New()
	f := &dhcpBulkFakeQ{serverFabricID: fabricID}
	req := httptest.NewRequest("POST", "/ipam/dhcp/servers/"+serverID.String()+"/scopes/push-all", nil)
	p := auth.Principal{
		Capabilities: []string{"ipam:dhcp-scopes:push"},
		Scopes: map[string]auth.Scope{
			"ipam:dhcp-scopes:push": {FabricIDs: map[uuid.UUID]struct{}{uuid.New(): {}}},
		},
	}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := &recordingAudit{}
	w := httptest.NewRecorder()
	mountDhcpBulk(f, rec).ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
	if len(rec.calls) != 0 {
		t.Errorf("audit fired on forbidden request: %+v", rec.calls)
	}
}

func TestPushAllDhcpScopes_AuditMetadataCarriesCounts(t *testing.T) {
	serverID := uuid.New()
	f := &dhcpBulkFakeQ{serverFabricID: uuid.New(), enabled: []uuid.UUID{uuid.New()}}
	rec := &recordingAudit{}
	w := runBulkRequest(t, "POST", "/ipam/dhcp/servers/"+serverID.String()+"/scopes/push-all", f, rec)
	_ = w
	if len(rec.calls) != 1 {
		t.Fatalf("audit calls = %d, want 1", len(rec.calls))
	}
	var meta map[string]any
	if err := json.Unmarshal(rec.calls[0].MetadataJson, &meta); err != nil {
		t.Fatalf("metadata decode: %v", err)
	}
	// Total is JSON-encoded as a number; decode lands it as float64.
	if total, _ := meta["total"].(float64); int(total) != 1 {
		t.Errorf("metadata.total = %v, want 1", meta["total"])
	}
	counts, ok := meta["counts"].(map[string]any)
	if !ok {
		t.Fatalf("metadata.counts not a map: %v", meta["counts"])
	}
	// Python's _tally pre-fills counts with dict.fromkeys(known, 0)
	// so every known status is present even when count is 0. Assert
	// every push status appears so a refactor that drops the zero-
	// fill (and silently breaks dashboards reading meta.counts.ok
	// when ok=0) is caught.
	for _, key := range []string{"ok", "error", "unsupported"} {
		v, present := counts[key]
		if !present {
			t.Errorf("metadata.counts missing %q (Python parity: dict.fromkeys)", key)
		}
		if _, isNumber := v.(float64); !isNumber {
			t.Errorf("metadata.counts[%q] = %v, want number", key, v)
		}
	}
}

// ---- push-drifted ----

func TestPushDriftedDhcpScopes_AuditActionMatchesPython(t *testing.T) {
	// Python uses "dhcp_scope.push_drifted" at api/ipam.py:2571.
	serverID := uuid.New()
	f := &dhcpBulkFakeQ{serverFabricID: uuid.New()}
	rec := &recordingAudit{}
	_ = runBulkRequest(t, "POST", "/ipam/dhcp/servers/"+serverID.String()+"/scopes/push-drifted", f, rec)
	if len(rec.calls) != 1 || rec.calls[0].Action != "dhcp_scope.push_drifted" {
		t.Errorf("audit action = %v", rec.calls)
	}
}

// ---- diff-all ----

func TestDiffAllDhcpScopes_NoAuditRecord(t *testing.T) {
	// Python's diff-all handler (line 2584-2608) deliberately does
	// not emit an audit row — the operation is a read, per-scope
	// persist side-effects are already captured in last_diff_at.
	// Go must match that posture so the audit stream doesn't fill
	// with noise from per-minute cron-style diff sweeps.
	serverID := uuid.New()
	f := &dhcpBulkFakeQ{serverFabricID: uuid.New()}
	rec := &recordingAudit{}
	_ = runBulkRequest(t, "GET", "/ipam/dhcp/servers/"+serverID.String()+"/scopes/diff-all", f, rec)
	if len(rec.calls) != 0 {
		t.Errorf("diff-all must not emit audit, got %+v", rec.calls)
	}
}

func TestDiffAllDhcpScopes_WireShape(t *testing.T) {
	// Python's response (line 2603) shape: server_id, total, counts,
	// results. Transitions is intentionally NOT in the API body even
	// though BulkDiffReport carries it.
	serverID := uuid.New()
	f := &dhcpBulkFakeQ{serverFabricID: uuid.New()}
	w := runBulkRequest(t, "GET", "/ipam/dhcp/servers/"+serverID.String()+"/scopes/diff-all", f, &recordingAudit{})
	body := w.Body.Bytes()
	for _, want := range [][]byte{
		[]byte(`"server_id":"` + serverID.String() + `"`),
		[]byte(`"total":0`),
		[]byte(`"counts":{`),
		[]byte(`"results":[]`),
	} {
		if !bytes.Contains(body, want) {
			t.Errorf("body missing %q, got %s", want, body)
		}
	}
	if bytes.Contains(body, []byte(`"transitions"`)) {
		t.Errorf("transitions must NOT be in API body, got %s", body)
	}
}

func TestDiffAllDhcpScopes_ServerNotFound_404(t *testing.T) {
	f := &dhcpBulkFakeQ{serverFabricErr: pgx.ErrNoRows}
	req := httptest.NewRequest("GET", "/ipam/dhcp/servers/"+uuid.New().String()+"/scopes/diff-all", nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountDhcpBulk(f, &recordingAudit{}).ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
