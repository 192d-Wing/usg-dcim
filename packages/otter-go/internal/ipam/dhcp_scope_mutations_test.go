// Handler tests for the DHCP scope mutation surface (PR 11):
//
//   POST   /ipam/dhcp/servers/{id}/scopes  CREATE
//   PATCH  /ipam/dhcp/scopes/{id}          partial UPDATE
//   DELETE /ipam/dhcp/scopes/{id}          soft-delete
//   POST   /ipam/dhcp/scopes/{id}/restore  undo soft-delete
//
// Focus areas:
//   - CREATE: URL/payload server_id mismatch → 400; ip_family
//     validation; v4 rejects pd_pools + preferred_lifetime; subnet
//     and template FK pre-checks (404 → 400); template family
//     mismatch; default Pools/Options/Reservations to [].
//   - UPDATE: existence + ABAC pre-fetch; v4-on-v4-scope guards;
//     pools "null" clears to []; pd_pools "null" clears to NULL;
//     audit diff only contains set keys; null name 400.
//   - DELETE: 404 on already-soft-deleted; Kea cleanup runs before
//     tombstone; audit captures kea_delete_status; 204 no body.
//   - RESTORE: 400 on already-live; clears deleted_at; no re-push.
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
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/push"
)

// dhcpMutFakeQ stitches together the methods the four mutation
// handlers reach. Other Querier methods inherit from fakeQ's noop
// embed. Captures every CreateDhcpScopeParams/UpdateDhcpScopeParams
// the handler emits so tests can assert on the SQL inputs.
type dhcpMutFakeQ struct {
	fakeQ

	serverFabricID uuid.UUID
	getResult      dbq.DhcpScope
	getErr         error

	subnetExists bool
	subnetID     uuid.UUID

	templateFamily int32
	templateExists bool

	createLast   dbq.CreateDhcpScopeParams
	createResult dbq.DhcpScope

	updateLast   dbq.UpdateDhcpScopeParams
	updateResult dbq.DhcpScope

	softDeleteCalls int
	restoreResult   dbq.DhcpScope
	restoreCalls    int
}

func (f *dhcpMutFakeQ) GetDhcpServerFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return f.serverFabricID, nil
}
func (f *dhcpMutFakeQ) GetDhcpScope(_ context.Context, _ uuid.UUID) (dbq.DhcpScope, error) {
	return f.getResult, f.getErr
}
func (f *dhcpMutFakeQ) GetSubnet(_ context.Context, id uuid.UUID) (dbq.Subnet, error) {
	if !f.subnetExists {
		return dbq.Subnet{}, pgx.ErrNoRows
	}
	return dbq.Subnet{ID: id}, nil
}
func (f *dhcpMutFakeQ) GetDhcpScopeTemplate(_ context.Context, id uuid.UUID) (dbq.DhcpScopeTemplate, error) {
	if !f.templateExists {
		return dbq.DhcpScopeTemplate{}, pgx.ErrNoRows
	}
	return dbq.DhcpScopeTemplate{ID: id, IPFamily: f.templateFamily}, nil
}
func (f *dhcpMutFakeQ) CreateDhcpScope(_ context.Context, a dbq.CreateDhcpScopeParams) (dbq.DhcpScope, error) {
	f.createLast = a
	return f.createResult, nil
}
func (f *dhcpMutFakeQ) UpdateDhcpScope(_ context.Context, a dbq.UpdateDhcpScopeParams) (dbq.DhcpScope, error) {
	f.updateLast = a
	return f.updateResult, nil
}
func (f *dhcpMutFakeQ) SoftDeleteDhcpScope(_ context.Context, _ uuid.UUID) error {
	f.softDeleteCalls++
	return nil
}
func (f *dhcpMutFakeQ) RestoreDhcpScope(_ context.Context, _ uuid.UUID) (dbq.DhcpScope, error) {
	f.restoreCalls++
	return f.restoreResult, nil
}

// Override push.Querier methods that DeleteScopeFromKea touches so
// the DELETE path doesn't fall into pgx.ErrNoRows from the noop fake
// and surface as kea.StatusError.
func (f *dhcpMutFakeQ) GetDhcpScopeForPush(_ context.Context, _ uuid.UUID) (dbq.DhcpScopeForPushRow, error) {
	if f.getResult.ID == uuid.Nil {
		return dbq.DhcpScopeForPushRow{}, pgx.ErrNoRows
	}
	return dbq.DhcpScopeForPushRow{
		ID:           f.getResult.ID,
		DhcpServerID: f.getResult.DhcpServerID,
		IPFamily:     f.getResult.IPFamily,
		Prefix:       f.getResult.Prefix,
		KeaSubnetID:  f.getResult.KeaSubnetID,
		Enabled:      f.getResult.Enabled,
	}, nil
}
func (f *dhcpMutFakeQ) GetDhcpServerForPush(_ context.Context, _ uuid.UUID) (dbq.DhcpServerForPushRow, error) {
	return dbq.DhcpServerForPushRow{ID: f.getResult.DhcpServerID, Enabled: true, KeaURL: "stub"}, nil
}

func mountDhcpMut(f *dhcpMutFakeQ, rec *recordingAudit) http.Handler {
	r := chi.NewRouter()
	(&Handler{
		Q:     f,
		Audit: rec,
		PushKea: func(_ dbq.DhcpServerForPushRow) push.KeaClient { return &fakeKea{} },
	}).Mount(r)
	return r
}

// runMutationRequest is the shared test setup boilerplate: build the
// request, set Content-Type, attach the wildcard principal, run the
// router, return the recorder. Pulled out of every assertion so the
// per-test bodies are 3-4 lines instead of 8-13, and the duplicate
// blocks SonarCloud flagged on PR 11 stay below threshold.
func runMutationRequest(t *testing.T, method, path string, body []byte, f *dhcpMutFakeQ, rec *recordingAudit) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	var req *http.Request
	if reader != nil {
		req = httptest.NewRequest(method, path, reader)
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountDhcpMut(f, rec).ServeHTTP(w, req)
	return w
}

// ---- CREATE ----

func TestCreateDhcpScope_HappyPathV4(t *testing.T) {
	srvID := uuid.New()
	f := &dhcpMutFakeQ{serverFabricID: uuid.New()}
	body, _ := json.Marshal(map[string]any{
		"dhcp_server_id": srvID, "name": "scope-1", "ip_family": 4, "prefix": "10.0.0.0/24",
		"pools": []map[string]any{{"first": "10.0.0.10", "last": "10.0.0.250"}},
	})
	rec := &recordingAudit{}
	w := runMutationRequest(t, "POST", "/ipam/dhcp/servers/"+srvID.String()+"/scopes", body, f, rec)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if f.createLast.IPFamily != 4 || f.createLast.Prefix != "10.0.0.0/24" {
		t.Errorf("create params = %+v", f.createLast)
	}
	// Pools threaded as JSON; Options + Reservations defaulted to [].
	if string(f.createLast.OptionsJSON) != "[]" {
		t.Errorf("options default = %s, want []", f.createLast.OptionsJSON)
	}
	if string(f.createLast.ReservationsJSON) != "[]" {
		t.Errorf("reservations default = %s, want []", f.createLast.ReservationsJSON)
	}
	if !f.createLast.Enabled {
		t.Errorf("enabled default = false, want true (Python default)")
	}
	if len(rec.calls) != 1 || rec.calls[0].Action != "dhcp_scope.create" {
		t.Errorf("audit = %+v", rec.calls)
	}
}

func TestCreateDhcpScope_PayloadServerMismatch_400(t *testing.T) {
	urlSrv := uuid.New()
	payloadSrv := uuid.New()
	f := &dhcpMutFakeQ{serverFabricID: uuid.New()}
	body, _ := json.Marshal(map[string]any{
		"dhcp_server_id": payloadSrv, "name": "x", "ip_family": 4, "prefix": "10.0.0.0/24",
	})
	w := runMutationRequest(t, "POST", "/ipam/dhcp/servers/"+urlSrv.String()+"/scopes", body, f, &recordingAudit{})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("must match URL")) {
		t.Errorf("body missing mismatch message, got %s", w.Body.String())
	}
}

func TestCreateDhcpScope_V4RejectsPdPools(t *testing.T) {
	srvID := uuid.New()
	f := &dhcpMutFakeQ{serverFabricID: uuid.New()}
	body, _ := json.Marshal(map[string]any{
		"dhcp_server_id": srvID, "name": "x", "ip_family": 4, "prefix": "10.0.0.0/24",
		"pd_pools": []map[string]any{{"prefix": "fd00::/64"}},
	})
	w := runMutationRequest(t, "POST", "/ipam/dhcp/servers/"+srvID.String()+"/scopes", body, f, &recordingAudit{})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("v6-only")) {
		t.Errorf("body missing v6-only error, got %s", w.Body.String())
	}
}

func TestCreateDhcpScope_TemplateFamilyMismatch_400(t *testing.T) {
	srvID := uuid.New()
	tplID := uuid.New()
	f := &dhcpMutFakeQ{
		serverFabricID: uuid.New(),
		templateExists: true,
		templateFamily: 6, // v6 template
	}
	body, _ := json.Marshal(map[string]any{
		"dhcp_server_id": srvID, "name": "x", "ip_family": 4, "prefix": "10.0.0.0/24",
		"template_id": tplID,
	})
	w := runMutationRequest(t, "POST", "/ipam/dhcp/servers/"+srvID.String()+"/scopes", body, f, &recordingAudit{})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("template ip_family")) {
		t.Errorf("body missing template family error, got %s", w.Body.String())
	}
}

func TestCreateDhcpScope_SubnetNotFound_400(t *testing.T) {
	srvID := uuid.New()
	f := &dhcpMutFakeQ{serverFabricID: uuid.New(), subnetExists: false}
	subID := uuid.New()
	body, _ := json.Marshal(map[string]any{
		"dhcp_server_id": srvID, "name": "x", "ip_family": 4, "prefix": "10.0.0.0/24",
		"subnet_id": subID,
	})
	w := runMutationRequest(t, "POST", "/ipam/dhcp/servers/"+srvID.String()+"/scopes", body, f, &recordingAudit{})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestCreateDhcpScope_V4ReservationWithDuid_400(t *testing.T) {
	srvID := uuid.New()
	f := &dhcpMutFakeQ{serverFabricID: uuid.New()}
	body, _ := json.Marshal(map[string]any{
		"dhcp_server_id": srvID, "name": "x", "ip_family": 4, "prefix": "10.0.0.0/24",
		"reservations": []map[string]any{{"duid": "00:01:00:01:abcd", "ip": "10.0.0.5"}},
	})
	w := runMutationRequest(t, "POST", "/ipam/dhcp/servers/"+srvID.String()+"/scopes", body, f, &recordingAudit{})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("v4 reservations use `mac`")) {
		t.Errorf("body missing v4-duid error, got %s", w.Body.String())
	}
}

func TestCreateDhcpScope_V6ReservationMissingDuid_400(t *testing.T) {
	srvID := uuid.New()
	f := &dhcpMutFakeQ{serverFabricID: uuid.New()}
	body, _ := json.Marshal(map[string]any{
		"dhcp_server_id": srvID, "name": "x", "ip_family": 6, "prefix": "2001:db8::/64",
		"reservations": []map[string]any{{"ip": "2001:db8::5"}},
	})
	w := runMutationRequest(t, "POST", "/ipam/dhcp/servers/"+srvID.String()+"/scopes", body, f, &recordingAudit{})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestCreateDhcpScope_PoolsExplicitNull_DefaultsToEmptyArray(t *testing.T) {
	// Python's Pydantic Create rejects pools=null with 422, but the
	// Go-side cutover treats explicit null the same as omitted —
	// defaults to []. Without this coercion the SQL INSERT would
	// write the literal `null` into a NOT NULL JSONB column and
	// downstream readers would see `"pools": null` instead of [].
	srvID := uuid.New()
	f := &dhcpMutFakeQ{serverFabricID: uuid.New()}
	body := []byte(`{"dhcp_server_id":"` + srvID.String() + `","name":"x","ip_family":4,"prefix":"10.0.0.0/24","pools":null,"options":null,"reservations":null}`)
	w := runMutationRequest(t, "POST", "/ipam/dhcp/servers/"+srvID.String()+"/scopes", body, f, &recordingAudit{})
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	for name, got := range map[string]string{
		"PoolsJSON":        string(f.createLast.PoolsJSON),
		"OptionsJSON":      string(f.createLast.OptionsJSON),
		"ReservationsJSON": string(f.createLast.ReservationsJSON),
	} {
		if got != "[]" {
			t.Errorf("%s = %q, want [] (explicit null must coerce)", name, got)
		}
	}
}

// ---- UPDATE ----

func TestUpdateDhcpScope_NullName_400(t *testing.T) {
	id := uuid.New()
	f := &dhcpMutFakeQ{
		serverFabricID: uuid.New(),
		getResult: dbq.DhcpScope{ID: id, DhcpServerID: uuid.New(), IPFamily: 4},
	}
	body := []byte(`{"name": null}`)
	w := runMutationRequest(t, "PATCH", "/ipam/dhcp/scopes/"+id.String(), body, f, &recordingAudit{})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestUpdateDhcpScope_V4RejectsPdPoolsOnExisting(t *testing.T) {
	id := uuid.New()
	f := &dhcpMutFakeQ{
		serverFabricID: uuid.New(),
		getResult: dbq.DhcpScope{ID: id, DhcpServerID: uuid.New(), IPFamily: 4},
	}
	body := []byte(`{"pd_pools": [{"prefix": "fd00::/64"}]}`)
	w := runMutationRequest(t, "PATCH", "/ipam/dhcp/scopes/"+id.String(), body, f, &recordingAudit{})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestUpdateDhcpScope_PoolsNullClearsToEmptyArray(t *testing.T) {
	id := uuid.New()
	srvID := uuid.New()
	f := &dhcpMutFakeQ{
		serverFabricID: uuid.New(),
		getResult: dbq.DhcpScope{ID: id, DhcpServerID: srvID, IPFamily: 4},
		updateResult: dbq.DhcpScope{ID: id, DhcpServerID: srvID, IPFamily: 4},
	}
	body := []byte(`{"pools": null}`)
	w := runMutationRequest(t, "PATCH", "/ipam/dhcp/scopes/"+id.String(), body, f, &recordingAudit{})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !f.updateLast.PoolsSet {
		t.Errorf("PoolsSet must be true for \"pools\":null")
	}
	if string(f.updateLast.PoolsJSON) != "[]" {
		t.Errorf("PoolsJSON = %s, want [] (Python parity for not-null column)", f.updateLast.PoolsJSON)
	}
}

func TestUpdateDhcpScope_PdPoolsNullClearsToNull(t *testing.T) {
	// pd_pools is genuinely nullable (v6-only column). A "null"
	// patch clears to SQL NULL, NOT [] like pools/options/reservations.
	id := uuid.New()
	srvID := uuid.New()
	f := &dhcpMutFakeQ{
		serverFabricID: uuid.New(),
		getResult: dbq.DhcpScope{ID: id, DhcpServerID: srvID, IPFamily: 6},
		updateResult: dbq.DhcpScope{ID: id, DhcpServerID: srvID, IPFamily: 6},
	}
	body := []byte(`{"pd_pools": null}`)
	w := runMutationRequest(t, "PATCH", "/ipam/dhcp/scopes/"+id.String(), body, f, &recordingAudit{})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !f.updateLast.PdPoolsSet {
		t.Errorf("PdPoolsSet must be true")
	}
	if len(f.updateLast.PdPoolsJSON) != 0 {
		t.Errorf("PdPoolsJSON = %s, want empty (clears to SQL NULL)", f.updateLast.PdPoolsJSON)
	}
}

func TestUpdateDhcpScope_AuditDiffOnlySetKeys(t *testing.T) {
	id := uuid.New()
	srvID := uuid.New()
	f := &dhcpMutFakeQ{
		serverFabricID: uuid.New(),
		getResult: dbq.DhcpScope{ID: id, DhcpServerID: srvID, IPFamily: 6},
		updateResult: dbq.DhcpScope{ID: id, DhcpServerID: srvID, IPFamily: 6},
	}
	body := []byte(`{"name": "renamed", "enabled": false}`)
	rec := &recordingAudit{}
	w := runMutationRequest(t, "PATCH", "/ipam/dhcp/scopes/"+id.String(), body, f, rec)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var diff map[string]any
	if err := json.Unmarshal(rec.calls[0].DiffJson, &diff); err != nil {
		t.Fatal(err)
	}
	if _, ok := diff["name"]; !ok {
		t.Errorf("diff missing name")
	}
	if _, ok := diff["enabled"]; !ok {
		t.Errorf("diff missing enabled")
	}
	for _, key := range []string{"pools", "pd_pools", "options", "reservations", "description"} {
		if _, ok := diff[key]; ok {
			t.Errorf("diff must NOT include unset key %q, got %v", key, diff[key])
		}
	}
}

// ---- DELETE ----

func TestDeleteDhcpScope_HappyPath_204(t *testing.T) {
	id := uuid.New()
	srvID := uuid.New()
	f := &dhcpMutFakeQ{
		serverFabricID: uuid.New(),
		getResult: dbq.DhcpScope{ID: id, DhcpServerID: srvID, IPFamily: 4, Prefix: "10.0.0.0/24", Enabled: true},
	}
	rec := &recordingAudit{}
	w := runMutationRequest(t, "DELETE", "/ipam/dhcp/scopes/"+id.String(), nil, f, rec)
	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", w.Code)
	}
	if f.softDeleteCalls != 1 {
		t.Errorf("SoftDelete calls = %d, want 1", f.softDeleteCalls)
	}
	if len(rec.calls) != 1 || rec.calls[0].Action != "dhcp_scope.delete" {
		t.Errorf("audit = %+v", rec.calls)
	}
	var meta map[string]any
	if err := json.Unmarshal(rec.calls[0].MetadataJson, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["soft_delete"] != true {
		t.Errorf("metadata soft_delete = %v, want true", meta["soft_delete"])
	}
	if _, ok := meta["kea_delete_status"]; !ok {
		t.Errorf("metadata missing kea_delete_status: %v", meta)
	}
}

func TestDeleteDhcpScope_AlreadySoftDeleted_404(t *testing.T) {
	id := uuid.New()
	deletedAt := time.Now()
	f := &dhcpMutFakeQ{
		serverFabricID: uuid.New(),
		getResult: dbq.DhcpScope{ID: id, DhcpServerID: uuid.New(), DeletedAt: &deletedAt},
	}
	w := runMutationRequest(t, "DELETE", "/ipam/dhcp/scopes/"+id.String(), nil, f, &recordingAudit{})
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (Python parity: missing + already-deleted collapse)", w.Code)
	}
	if f.softDeleteCalls != 0 {
		t.Errorf("must not call SoftDelete on already-tombstoned row")
	}
}

// ---- RESTORE ----

func TestRestoreDhcpScope_HappyPath_200(t *testing.T) {
	id := uuid.New()
	srvID := uuid.New()
	deletedAt := time.Now()
	f := &dhcpMutFakeQ{
		serverFabricID: uuid.New(),
		getResult: dbq.DhcpScope{ID: id, DhcpServerID: srvID, IPFamily: 4, Prefix: "10.0.0.0/24", DeletedAt: &deletedAt},
		restoreResult: dbq.DhcpScope{ID: id, DhcpServerID: srvID, IPFamily: 4, Prefix: "10.0.0.0/24"},
	}
	rec := &recordingAudit{}
	w := runMutationRequest(t, "POST", "/ipam/dhcp/scopes/"+id.String()+"/restore", nil, f, rec)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if f.restoreCalls != 1 {
		t.Errorf("Restore calls = %d, want 1", f.restoreCalls)
	}
	if len(rec.calls) != 1 || rec.calls[0].Action != "dhcp_scope.restore" {
		t.Errorf("audit = %+v", rec.calls)
	}
}

func TestRestoreDhcpScope_NotSoftDeleted_400(t *testing.T) {
	id := uuid.New()
	f := &dhcpMutFakeQ{
		serverFabricID: uuid.New(),
		getResult: dbq.DhcpScope{ID: id, DhcpServerID: uuid.New(), DeletedAt: nil},
	}
	w := runMutationRequest(t, "POST", "/ipam/dhcp/scopes/"+id.String()+"/restore", nil, f, &recordingAudit{})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestRestoreDhcpScope_NotFound_404(t *testing.T) {
	f := &dhcpMutFakeQ{
		serverFabricID: uuid.New(),
		getErr:         pgx.ErrNoRows,
	}
	w := runMutationRequest(t, "POST", "/ipam/dhcp/scopes/"+uuid.New().String()+"/restore", nil, f, &recordingAudit{})
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
