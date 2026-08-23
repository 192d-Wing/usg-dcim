// Handler tests for /dhcp/servers/{id}/scopes and /dhcp/scopes/{id}
// (PR 10 of the DHCP push port). Read paths only — mutation tests
// ship with the CREATE/PATCH/DELETE/RESTORE PR.
//
// Focus areas:
//   - LIST: every filter threads through (server_id, ip_family,
//     enabled, diff_status, include_deleted), bad values 400.
//   - GET: 404 on missing, 403 when fabric not in scope, returns
//     soft-deleted scopes (response carries deleted_at).
//   - Wire shape: dbq's `*_json` columns rename to Python-canonical
//     fields (pools/options/etc.); timestamps use the Python
//     isoformat parity layout.
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
)

type dhcpScopesFakeQ struct {
	fakeQ

	scopeFabricID  uuid.UUID
	scopeFabricErr error
	serverFabricID uuid.UUID

	listResult []dbq.ListDhcpScopesByServerRow
	listLast   dbq.ListDhcpScopesByServerParams
	listErr    error

	countResult int64
	countLast   dbq.CountDhcpScopesByServerParams

	getResult dbq.GetDhcpScopeRow
	getErr    error
}

func (f *dhcpScopesFakeQ) GetDhcpScopeFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return f.scopeFabricID, f.scopeFabricErr
}
func (f *dhcpScopesFakeQ) GetDhcpServerFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return f.serverFabricID, nil
}
func (f *dhcpScopesFakeQ) ListDhcpScopesByServer(_ context.Context, a dbq.ListDhcpScopesByServerParams) ([]dbq.ListDhcpScopesByServerRow, error) {
	f.listLast = a
	return f.listResult, f.listErr
}
func (f *dhcpScopesFakeQ) CountDhcpScopesByServer(_ context.Context, a dbq.CountDhcpScopesByServerParams) (int64, error) {
	f.countLast = a
	return f.countResult, nil
}
func (f *dhcpScopesFakeQ) GetDhcpScope(_ context.Context, _ uuid.UUID) (dbq.GetDhcpScopeRow, error) {
	return f.getResult, f.getErr
}

func mountDhcpScopes(f *dhcpScopesFakeQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}

// ---- LIST ----

func TestListDhcpScopes_AllFilters(t *testing.T) {
	srvID := uuid.New()
	f := &dhcpScopesFakeQ{serverFabricID: uuid.New()}
	req := httptest.NewRequest("GET",
		"/ipam/dhcp/servers/"+srvID.String()+"/scopes?ip_family=4&enabled=true&diff_status=drifted&include_deleted=true",
		nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountDhcpScopes(f).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if f.listLast.DhcpServerID != srvID {
		t.Errorf("server id not threaded: got %v want %v", f.listLast.DhcpServerID, srvID)
	}
	if f.listLast.IPFamily == nil || *f.listLast.IPFamily != 4 {
		t.Errorf("ip_family = %v", f.listLast.IPFamily)
	}
	if f.listLast.Enabled == nil || *f.listLast.Enabled != true {
		t.Errorf("enabled = %v", f.listLast.Enabled)
	}
	if f.listLast.DiffStatus == nil || *f.listLast.DiffStatus != "drifted" {
		t.Errorf("diff_status = %v", f.listLast.DiffStatus)
	}
	if !f.listLast.IncludeDeleted {
		t.Errorf("include_deleted not threaded")
	}
}

func TestListDhcpScopes_BadFilters_400(t *testing.T) {
	srvID := uuid.New()
	f := &dhcpScopesFakeQ{serverFabricID: uuid.New()}
	for _, q := range []string{
		"ip_family=5",
		"enabled=yes",
		"diff_status=unknown",
	} {
		req := httptest.NewRequest("GET",
			"/ipam/dhcp/servers/"+srvID.String()+"/scopes?"+q, nil)
		req = withPrincipal(req, "*")
		w := httptest.NewRecorder()
		mountDhcpScopes(f).ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("query %q: status = %d, want 400", q, w.Code)
		}
	}
}

func TestListDhcpScopes_ForbiddenWithoutScopedCap(t *testing.T) {
	srvID := uuid.New()
	fabricID := uuid.New()
	f := &dhcpScopesFakeQ{serverFabricID: fabricID}
	req := httptest.NewRequest("GET", "/ipam/dhcp/servers/"+srvID.String()+"/scopes", nil)
	p := auth.Principal{
		Capabilities: []string{"ipam:dhcp-scopes:read"},
		Scopes: map[string]auth.Scope{
			"ipam:dhcp-scopes:read": {FabricIDs: map[uuid.UUID]struct{}{uuid.New(): {}}},
		},
	}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	w := httptest.NewRecorder()
	mountDhcpScopes(f).ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestListDhcpScopes_DefaultExcludesSoftDeleted(t *testing.T) {
	srvID := uuid.New()
	f := &dhcpScopesFakeQ{serverFabricID: uuid.New()}
	req := httptest.NewRequest("GET", "/ipam/dhcp/servers/"+srvID.String()+"/scopes", nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountDhcpScopes(f).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if f.listLast.IncludeDeleted {
		t.Errorf("default include_deleted must be false (Python parity)")
	}
}

// ---- GET ----

func TestGetDhcpScope_NotFound_404(t *testing.T) {
	f := &dhcpScopesFakeQ{
		scopeFabricID: uuid.New(),
		getErr:        pgx.ErrNoRows,
	}
	req := httptest.NewRequest("GET", "/ipam/dhcp/scopes/"+uuid.New().String(), nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountDhcpScopes(f).ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestGetDhcpScope_FabricMissing_404(t *testing.T) {
	// 2-hop fabric lookup misses → 404 (matches the per-scope push
	// endpoint's collapsed scope-or-server-gone shape from PR 5).
	f := &dhcpScopesFakeQ{scopeFabricErr: pgx.ErrNoRows}
	req := httptest.NewRequest("GET", "/ipam/dhcp/scopes/"+uuid.New().String(), nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountDhcpScopes(f).ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestGetDhcpScope_ReturnsSoftDeletedScope(t *testing.T) {
	// Python's get_dhcp_scope returns tombstoned rows so the client
	// can decide whether to restore. Regression guard: a future
	// change that filters deleted_at IS NULL at the SQL level would
	// break the restore workflow.
	scopeID := uuid.New()
	deletedAt := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	f := &dhcpScopesFakeQ{
		scopeFabricID: uuid.New(),
		getResult: dbq.GetDhcpScopeRow{
			ID: scopeID, IPFamily: 4, Prefix: "10.0.0.0/24",
			DeletedAt: &deletedAt,
			PoolsJSON: json.RawMessage(`[]`),
			OptionsJSON: json.RawMessage(`[]`),
			CreatedAt:   time.Now(), UpdatedAt: time.Now(),
		},
	}
	req := httptest.NewRequest("GET", "/ipam/dhcp/scopes/"+scopeID.String(), nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountDhcpScopes(f).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"deleted_at":"2026-05-01T12:00:00.000000+00:00"`)) {
		t.Errorf("body must carry deleted_at, got %s", w.Body.String())
	}
}

func TestGetDhcpScope_WireShape_PythonCanonicalFields(t *testing.T) {
	// Catch the dbq tag leak (pools_json / options_json / etc.) that
	// would silently break a client coded against the Python shape.
	scopeID := uuid.New()
	f := &dhcpScopesFakeQ{
		scopeFabricID: uuid.New(),
		getResult: dbq.GetDhcpScopeRow{
			ID: scopeID, IPFamily: 4, Prefix: "10.0.0.0/24",
			PoolsJSON:        json.RawMessage(`[{"first":"10.0.0.10","last":"10.0.0.250"}]`),
			OptionsJSON:      json.RawMessage(`[]`),
			ReservationsJSON: json.RawMessage(`[]`),
			CreatedAt:        time.Now(), UpdatedAt: time.Now(),
		},
	}
	req := httptest.NewRequest("GET", "/ipam/dhcp/scopes/"+scopeID.String(), nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountDhcpScopes(f).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.Bytes()
	for _, want := range [][]byte{
		[]byte(`"pools":[`),
		[]byte(`"pd_pools":null`),
		[]byte(`"options":[`),
		[]byte(`"reservations":[`),
	} {
		if !bytes.Contains(body, want) {
			t.Errorf("body missing %q, got %s", want, body)
		}
	}
	for _, leak := range [][]byte{
		[]byte(`"pools_json"`),
		[]byte(`"pd_pools_json"`),
		[]byte(`"options_json"`),
		[]byte(`"reservations_json"`),
		[]byte(`"last_diff_delta_json"`),
	} {
		if bytes.Contains(body, leak) {
			t.Errorf("dbq tag leak %q in response, got %s", leak, body)
		}
	}
}

func TestGetDhcpScope_NullJSONColumns_RenderAsPythonShape(t *testing.T) {
	// Schema declares pools/options/reservations NOT NULL with
	// default=list; Python's model further coerces `column_json or
	// []` (models/ipam.py:625, :633, :637). So even legacy/manual
	// NULL JSONB rows emit [] on the Python wire. pd_pools and
	// last_diff_delta stay genuinely nullable (schemas/ipam.py:387,
	// 444). The rawJSONArray vs rawJSON split mirrors that.
	scopeID := uuid.New()
	f := &dhcpScopesFakeQ{
		scopeFabricID: uuid.New(),
		getResult: dbq.GetDhcpScopeRow{
			ID: scopeID, IPFamily: 6, Prefix: "2001:db8::/64",
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		},
	}
	req := httptest.NewRequest("GET", "/ipam/dhcp/scopes/"+scopeID.String(), nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountDhcpScopes(f).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	for _, want := range [][]byte{
		[]byte(`"pools":[]`),
		[]byte(`"options":[]`),
		[]byte(`"reservations":[]`),
		[]byte(`"pd_pools":null`),
		[]byte(`"last_diff_delta":null`),
	} {
		if !bytes.Contains(w.Body.Bytes(), want) {
			t.Errorf("body missing %q, got %s", want, w.Body.String())
		}
	}
}

func TestListDhcpScopes_WireShapeAndCountFilterPropagate(t *testing.T) {
	srvID := uuid.New()
	scopeID := uuid.New()
	f := &dhcpScopesFakeQ{
		serverFabricID: uuid.New(),
		listResult: []dbq.ListDhcpScopesByServerRow{
			{
				ID: scopeID, DhcpServerID: srvID, IPFamily: 4, Prefix: "10.0.0.0/24",
				PoolsJSON:        json.RawMessage(`[{"first":"10.0.0.10","last":"10.0.0.250"}]`),
				OptionsJSON:      json.RawMessage(`[]`),
				ReservationsJSON: json.RawMessage(`[]`),
				CreatedAt:        time.Now(), UpdatedAt: time.Now(),
			},
		},
		countResult: 1,
	}
	req := httptest.NewRequest("GET",
		"/ipam/dhcp/servers/"+srvID.String()+"/scopes?ip_family=4&enabled=true&diff_status=drifted&include_deleted=true",
		nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountDhcpScopes(f).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	// LIST items get the same rename + dbq-tag-leak guards as GET —
	// catches a regression that swaps the page item type from
	// dhcpScopeOut back to dbq.DhcpScope.
	body := w.Body.Bytes()
	if !bytes.Contains(body, []byte(`"pools":[{`)) {
		t.Errorf("LIST body missing \"pools\":[…], got %s", body)
	}
	if bytes.Contains(body, []byte(`"pools_json"`)) {
		t.Errorf("LIST body leaks dbq tag \"pools_json\", got %s", body)
	}
	// COUNT must receive the same filter set as LIST so Page.Total
	// reflects the filtered total — a regression that passes
	// unfiltered Count would over-count and break pagination.
	if f.countLast.IncludeDeleted != true {
		t.Errorf("count include_deleted not propagated: %+v", f.countLast)
	}
	if f.countLast.IPFamily == nil || *f.countLast.IPFamily != 4 {
		t.Errorf("count ip_family not propagated: %v", f.countLast.IPFamily)
	}
	if f.countLast.Enabled == nil || *f.countLast.Enabled != true {
		t.Errorf("count enabled not propagated: %v", f.countLast.Enabled)
	}
	if f.countLast.DiffStatus == nil || *f.countLast.DiffStatus != "drifted" {
		t.Errorf("count diff_status not propagated: %v", f.countLast.DiffStatus)
	}
}
