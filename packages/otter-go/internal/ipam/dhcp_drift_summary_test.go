// Handler tests for /dhcp/drift-summary (PR 9). The aggregation
// math is covered exhaustively in internal/dhcp/driftsummary; these
// tests focus on the HTTP-layer concerns:
//
//   - 200 with empty-fleet shape when ABAC scope is empty (no DB call)
//   - 200 with empty-fleet shape when scope is non-empty but the
//     fleet has no servers
//   - ScopeFabricIds threaded into the server-list query
//   - All three SELECTs participate; failures surface as 5xx via
//     httpx.Mapped
//   - Response wire shape: top-level keys, fixed scope_counts keys
package ipam

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

type driftSummaryFakeQ struct {
	fakeQ

	servers       []dbq.DhcpServerDriftSummaryRow
	serversErr    error
	serversLastIn []uuid.UUID

	scopes         []dbq.DhcpScopeDriftStatusRow
	scopesErr      error
	scopesLastArgs []uuid.UUID

	alertKeys    []string
	alertKeysErr error
}

func (f *driftSummaryFakeQ) ListDhcpServersForDriftSummary(_ context.Context, scopeFabricIds []uuid.UUID) ([]dbq.DhcpServerDriftSummaryRow, error) {
	f.serversLastIn = scopeFabricIds
	return f.servers, f.serversErr
}
func (f *driftSummaryFakeQ) ListDhcpScopeDriftStatusByServers(_ context.Context, serverIDs []uuid.UUID) ([]dbq.DhcpScopeDriftStatusRow, error) {
	f.scopesLastArgs = serverIDs
	return f.scopes, f.scopesErr
}
func (f *driftSummaryFakeQ) ListFiringDhcpDriftAlertKeys(_ context.Context) ([]string, error) {
	return f.alertKeys, f.alertKeysErr
}

func mountDriftSummary(f *driftSummaryFakeQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}

func TestDhcpDriftSummary_EmptyScope_ShortCircuitsWithoutDBCall(t *testing.T) {
	f := &driftSummaryFakeQ{}
	req := httptest.NewRequest("GET", "/ipam/dhcp/drift-summary", nil)
	p := auth.Principal{
		Capabilities: []string{"ipam:dhcp-scopes:read"},
		Scopes: map[string]auth.Scope{
			"ipam:dhcp-scopes:read": {FabricIDs: map[uuid.UUID]struct{}{}},
		},
	}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	w := httptest.NewRecorder()
	mountDriftSummary(f).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if f.serversLastIn != nil {
		t.Errorf("ListDhcpServersForDriftSummary must not have been called, got %v", f.serversLastIn)
	}
	// No-scope shape: fleet + servers only, NO fabrics key.
	// Python's first short-circuit (api/ipam.py:2632-2645) omits
	// `fabrics`; the second short-circuit (no servers, scope was
	// non-empty) keeps it.
	body := w.Body.Bytes()
	for _, want := range [][]byte{
		[]byte(`"servers_total":0`),
		[]byte(`"in_sync":0`),
		[]byte(`"drifted":0`),
		[]byte(`"never_pushed":0`),
		[]byte(`"servers":[]`),
	} {
		if !bytes.Contains(body, want) {
			t.Errorf("body missing %q, got %s", want, body)
		}
	}
	if bytes.Contains(body, []byte(`"fabrics"`)) {
		t.Errorf("no-scope branch must NOT emit \"fabrics\" key (Python parity), got %s", body)
	}
}

func TestDhcpDriftSummary_NoServers_ShortCircuitsAfterServerList(t *testing.T) {
	f := &driftSummaryFakeQ{servers: nil}
	req := httptest.NewRequest("GET", "/ipam/dhcp/drift-summary", nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountDriftSummary(f).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	// Empty-fleet shape, but ListDhcpServersForDriftSummary WAS
	// called (scope was non-empty).
	if f.scopesLastArgs != nil {
		t.Errorf("must not list scopes when no servers, got %v", f.scopesLastArgs)
	}
}

func TestDhcpDriftSummary_ScopedCallerThreadsFabricIDs(t *testing.T) {
	allowed := uuid.New()
	f := &driftSummaryFakeQ{servers: nil}
	req := httptest.NewRequest("GET", "/ipam/dhcp/drift-summary", nil)
	p := auth.Principal{
		Capabilities: []string{"ipam:dhcp-scopes:read"},
		Scopes: map[string]auth.Scope{
			"ipam:dhcp-scopes:read": {FabricIDs: map[uuid.UUID]struct{}{allowed: {}}},
		},
	}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	w := httptest.NewRecorder()
	mountDriftSummary(f).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if len(f.serversLastIn) != 1 || f.serversLastIn[0] != allowed {
		t.Errorf("scope_fabric_ids = %v, want [%s]", f.serversLastIn, allowed)
	}
}

func TestDhcpDriftSummary_HappyPath_FullPipeline(t *testing.T) {
	srvID := uuid.New()
	scopeID := uuid.New()
	fabricID := uuid.New()
	f := &driftSummaryFakeQ{
		servers: []dbq.DhcpServerDriftSummaryRow{
			{ID: srvID, Name: "kea-1", FabricID: fabricID, Enabled: true},
		},
		scopes: []dbq.DhcpScopeDriftStatusRow{
			{ID: scopeID, DhcpServerID: srvID, LastDiffStatus: ptrStr("drifted")},
		},
		alertKeys: []string{"dhcp-drift:" + scopeID.String()},
	}
	req := httptest.NewRequest("GET", "/ipam/dhcp/drift-summary", nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountDriftSummary(f).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	// Server-list query must have run; scope query must have been
	// called with the server's ID; alert query is always run on the
	// non-empty path.
	if len(f.scopesLastArgs) != 1 || f.scopesLastArgs[0] != srvID {
		t.Errorf("scope query args = %v, want [%s]", f.scopesLastArgs, srvID)
	}
	var got dhcpDriftSummaryBody
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Fleet.ServersTotal != 1 || got.Fleet.ServersWithDrift != 1 {
		t.Errorf("fleet = %+v", got.Fleet)
	}
	if got.Fleet.AlertsFiring != 1 {
		t.Errorf("alerts_firing = %d, want 1 (one dhcp-drift alert keyed to the drifted scope)", got.Fleet.AlertsFiring)
	}
	if len(got.Fabrics) != 1 || got.Fabrics[0].FabricID != fabricID.String() {
		t.Errorf("fabrics = %+v", got.Fabrics)
	}
	if len(got.Servers) != 1 || got.Servers[0].ServerID != srvID.String() {
		t.Errorf("servers = %+v", got.Servers)
	}
}

func TestDhcpDriftSummary_ServerListError_500(t *testing.T) {
	f := &driftSummaryFakeQ{serversErr: errors.New("pgx down")}
	req := httptest.NewRequest("GET", "/ipam/dhcp/drift-summary", nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountDriftSummary(f).ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

func TestDhcpDriftSummary_AlertQueryError_500(t *testing.T) {
	srvID := uuid.New()
	f := &driftSummaryFakeQ{
		servers:      []dbq.DhcpServerDriftSummaryRow{{ID: srvID, Name: "kea-1", FabricID: uuid.New()}},
		alertKeysErr: errors.New("alerts table missing"),
	}
	req := httptest.NewRequest("GET", "/ipam/dhcp/drift-summary", nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountDriftSummary(f).ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

func ptrStr(s string) *string { return &s }
