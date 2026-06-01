// Handler tests for the DHCP scope-template CRUD surface (PR 8).
// Focus areas:
//
//   - LIST: ABAC scope filter applied; fabric_id + ip_family filters
//     thread through to params; empty scope short-circuits to {items:[]}.
//   - GET: 404 on missing, 403 when fabric not in scope.
//   - POST: ip_family validation, v4 rejects preferred_lifetime, fabric
//     pre-check 404, audit metadata shape, options defaults to [].
//   - PATCH: preferred_lifetime guard against existing v4 row, audit
//     diff only contains keys the patch set, "options":null clears.
//   - DELETE: existence + ABAC, audit metadata, 204 no body.
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
)

// dhcpTemplateFakeQ overrides only the template-specific methods +
// GetFabric (needed by the create pre-check). Other Querier methods
// inherit from fakeQ's noop embed.
type dhcpTemplateFakeQ struct {
	fakeQ

	listResult []dbq.DhcpScopeTemplate
	listErr    error
	listLast   dbq.ListDhcpScopeTemplatesParams

	countResult int64
	countLast   dbq.CountDhcpScopeTemplatesParams

	getResult dbq.DhcpScopeTemplate
	getErr    error

	createResult dbq.DhcpScopeTemplate
	createLast   dbq.CreateDhcpScopeTemplateParams
	createErr    error

	updateResult dbq.DhcpScopeTemplate
	updateLast   dbq.UpdateDhcpScopeTemplateParams
	updateErr    error

	deleteCount int
	deleteErr   error

	fabricExists bool
}

func (f *dhcpTemplateFakeQ) ListDhcpScopeTemplates(_ context.Context, a dbq.ListDhcpScopeTemplatesParams) ([]dbq.DhcpScopeTemplate, error) {
	f.listLast = a
	return f.listResult, f.listErr
}
func (f *dhcpTemplateFakeQ) CountDhcpScopeTemplates(_ context.Context, a dbq.CountDhcpScopeTemplatesParams) (int64, error) {
	f.countLast = a
	return f.countResult, nil
}
func (f *dhcpTemplateFakeQ) GetDhcpScopeTemplate(_ context.Context, _ uuid.UUID) (dbq.DhcpScopeTemplate, error) {
	return f.getResult, f.getErr
}
func (f *dhcpTemplateFakeQ) CreateDhcpScopeTemplate(_ context.Context, a dbq.CreateDhcpScopeTemplateParams) (dbq.DhcpScopeTemplate, error) {
	f.createLast = a
	return f.createResult, f.createErr
}
func (f *dhcpTemplateFakeQ) UpdateDhcpScopeTemplate(_ context.Context, a dbq.UpdateDhcpScopeTemplateParams) (dbq.DhcpScopeTemplate, error) {
	f.updateLast = a
	return f.updateResult, f.updateErr
}
func (f *dhcpTemplateFakeQ) DeleteDhcpScopeTemplate(_ context.Context, _ uuid.UUID) error {
	f.deleteCount++
	return f.deleteErr
}
func (f *dhcpTemplateFakeQ) GetFabric(_ context.Context, _ uuid.UUID) (dbq.Fabric, error) {
	if !f.fabricExists {
		return dbq.Fabric{}, pgx.ErrNoRows
	}
	return dbq.Fabric{ID: uuid.New()}, nil
}

func mountTemplates(f *dhcpTemplateFakeQ, rec *recordingAudit) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f, Audit: rec}).Mount(r)
	return r
}

// ---- LIST ----

func TestListDhcpScopeTemplates_FabricAndIPFamilyFilter(t *testing.T) {
	fid := uuid.New()
	f := &dhcpTemplateFakeQ{}
	rec := &recordingAudit{}
	req := httptest.NewRequest("GET", "/ipam/dhcp/scope-templates?fabric_id="+fid.String()+"&ip_family=6", nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountTemplates(f, rec).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if f.listLast.FabricID == nil || *f.listLast.FabricID != fid {
		t.Errorf("fabric_id not threaded: %v", f.listLast.FabricID)
	}
	if f.listLast.IPFamily == nil || *f.listLast.IPFamily != 6 {
		t.Errorf("ip_family = %v, want 6", f.listLast.IPFamily)
	}
}

func TestListDhcpScopeTemplates_BadIPFamily_400(t *testing.T) {
	req := httptest.NewRequest("GET", "/ipam/dhcp/scope-templates?ip_family=5", nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountTemplates(&dhcpTemplateFakeQ{}, &recordingAudit{}).ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// A scoped caller with one fabric grant must have ScopeFabricIds
// threaded into ListDhcpScopeTemplatesParams so the SQL
// `fabric_id = ANY($5)` filter scopes the result set. Catches a
// regression where a future query refactor drops the narg or its
// binding.
func TestListDhcpScopeTemplates_ScopedCaller_ThreadsFabricFilter(t *testing.T) {
	allowedFabric := uuid.New()
	f := &dhcpTemplateFakeQ{}
	req := httptest.NewRequest("GET", "/ipam/dhcp/scope-templates", nil)
	p := auth.Principal{
		Capabilities: []string{"ipam:dhcp-scope-templates:read"},
		Scopes: map[string]auth.Scope{
			"ipam:dhcp-scope-templates:read": {FabricIDs: map[uuid.UUID]struct{}{allowedFabric: {}}},
		},
	}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	w := httptest.NewRecorder()
	mountTemplates(f, &recordingAudit{}).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if len(f.listLast.ScopeFabricIds) != 1 || f.listLast.ScopeFabricIds[0] != allowedFabric {
		t.Errorf("ScopeFabricIds = %v, want [%s]", f.listLast.ScopeFabricIds, allowedFabric)
	}
	if len(f.countLast.ScopeFabricIds) != 1 || f.countLast.ScopeFabricIds[0] != allowedFabric {
		t.Errorf("Count ScopeFabricIds = %v, want [%s]", f.countLast.ScopeFabricIds, allowedFabric)
	}
}

func TestListDhcpScopeTemplates_EmptyScope_ReturnsEmptyPage(t *testing.T) {
	req := httptest.NewRequest("GET", "/ipam/dhcp/scope-templates", nil)
	p := auth.Principal{
		Capabilities: []string{"ipam:dhcp-scope-templates:read"},
		Scopes: map[string]auth.Scope{
			"ipam:dhcp-scope-templates:read": {FabricIDs: map[uuid.UUID]struct{}{}},
		},
	}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	w := httptest.NewRecorder()
	f := &dhcpTemplateFakeQ{}
	mountTemplates(f, &recordingAudit{}).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	// Empty-scope short-circuit must NOT hit the DB at all.
	if f.listLast.Limit != 0 {
		t.Errorf("list should not have been called; got params = %+v", f.listLast)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"items":[]`)) {
		t.Errorf("body must contain \"items\":[], got %s", w.Body.String())
	}
}

// ---- GET ----

func TestGetDhcpScopeTemplate_NotFound(t *testing.T) {
	f := &dhcpTemplateFakeQ{getErr: pgx.ErrNoRows}
	req := httptest.NewRequest("GET", "/ipam/dhcp/scope-templates/"+uuid.New().String(), nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountTemplates(f, &recordingAudit{}).ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestGetDhcpScopeTemplate_ForbiddenWithoutScopedCap(t *testing.T) {
	fabricID := uuid.New()
	f := &dhcpTemplateFakeQ{getResult: dbq.DhcpScopeTemplate{ID: uuid.New(), FabricID: fabricID}}
	req := httptest.NewRequest("GET", "/ipam/dhcp/scope-templates/"+uuid.New().String(), nil)
	p := auth.Principal{
		Capabilities: []string{"ipam:dhcp-scope-templates:read"},
		Scopes: map[string]auth.Scope{
			"ipam:dhcp-scope-templates:read": {FabricIDs: map[uuid.UUID]struct{}{uuid.New(): {}}},
		},
	}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	w := httptest.NewRecorder()
	mountTemplates(f, &recordingAudit{}).ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

// ---- POST ----

func TestCreateDhcpScopeTemplate_HappyPath_V4(t *testing.T) {
	fid := uuid.New()
	out := dbq.DhcpScopeTemplate{ID: uuid.New(), FabricID: fid, Name: "tpl", IPFamily: 4}
	f := &dhcpTemplateFakeQ{fabricExists: true, createResult: out}
	body, _ := json.Marshal(map[string]any{
		"fabric_id": fid, "name": "tpl", "ip_family": 4,
		"options": []map[string]any{{"code": 3, "data": "10.0.0.1"}},
	})
	rec := &recordingAudit{}
	req := httptest.NewRequest("POST", "/ipam/dhcp/scope-templates", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountTemplates(f, rec).ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if f.createLast.Name != "tpl" || f.createLast.IPFamily != 4 || f.createLast.FabricID != fid {
		t.Errorf("create params = %+v", f.createLast)
	}
	if len(rec.calls) != 1 || rec.calls[0].Action != "dhcp_scope_template.create" {
		t.Errorf("audit action: %+v", rec.calls)
	}
}

func TestCreateDhcpScopeTemplate_V4WithPreferredLifetime_400(t *testing.T) {
	fid := uuid.New()
	preferred := int32(1000)
	body, _ := json.Marshal(map[string]any{
		"fabric_id": fid, "name": "bad", "ip_family": 4,
		"preferred_lifetime_seconds": preferred,
	})
	req := httptest.NewRequest("POST", "/ipam/dhcp/scope-templates", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountTemplates(&dhcpTemplateFakeQ{}, &recordingAudit{}).ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("v6-only")) {
		t.Errorf("body missing v6-only error, got %s", w.Body.String())
	}
}

func TestCreateDhcpScopeTemplate_BadIPFamily_400(t *testing.T) {
	fid := uuid.New()
	body, _ := json.Marshal(map[string]any{
		"fabric_id": fid, "name": "x", "ip_family": 5,
	})
	req := httptest.NewRequest("POST", "/ipam/dhcp/scope-templates", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountTemplates(&dhcpTemplateFakeQ{}, &recordingAudit{}).ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestCreateDhcpScopeTemplate_FabricMissing_404(t *testing.T) {
	fid := uuid.New()
	f := &dhcpTemplateFakeQ{fabricExists: false}
	body, _ := json.Marshal(map[string]any{
		"fabric_id": fid, "name": "x", "ip_family": 4,
	})
	req := httptest.NewRequest("POST", "/ipam/dhcp/scope-templates", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountTemplates(f, &recordingAudit{}).ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestCreateDhcpScopeTemplate_OmittedOptionsDefaultsToEmptyArray(t *testing.T) {
	fid := uuid.New()
	f := &dhcpTemplateFakeQ{fabricExists: true}
	body, _ := json.Marshal(map[string]any{
		"fabric_id": fid, "name": "x", "ip_family": 4,
	})
	req := httptest.NewRequest("POST", "/ipam/dhcp/scope-templates", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountTemplates(f, &recordingAudit{}).ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d", w.Code)
	}
	if string(f.createLast.OptionsJSON) != "[]" {
		t.Errorf("options_json = %s, want []", f.createLast.OptionsJSON)
	}
}

// ---- PATCH ----

// Null name on PATCH must be rejected; Python's PATCH writes
// `name=None` and the NOT NULL constraint raises. Go's COALESCE
// silently keeps the current value — masking the client bug.
// Explicit 400 with a descriptive message matches Python's posture
// (operation rejected) and gives a better error than a 500.
func TestUpdateDhcpScopeTemplate_NullName_400(t *testing.T) {
	fid := uuid.New()
	tid := uuid.New()
	f := &dhcpTemplateFakeQ{
		getResult: dbq.DhcpScopeTemplate{ID: tid, FabricID: fid, IPFamily: 6},
	}
	body := []byte(`{"name": null}`)
	req := httptest.NewRequest("PATCH", "/ipam/dhcp/scope-templates/"+tid.String(), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountTemplates(f, &recordingAudit{}).ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// "options": null in the audit diff must record JSON null, not [].
// Python's exclude_unset dump keeps None distinct from []; an
// auditor distinguishing "operator cleared" from "operator posted
// empty" needs that signal.
func TestUpdateDhcpScopeTemplate_AuditDiffPreservesNullVsEmpty(t *testing.T) {
	fid := uuid.New()
	tid := uuid.New()
	f := &dhcpTemplateFakeQ{
		getResult:    dbq.DhcpScopeTemplate{ID: tid, FabricID: fid, IPFamily: 6},
		updateResult: dbq.DhcpScopeTemplate{ID: tid, FabricID: fid, IPFamily: 6},
	}
	body := []byte(`{"options": null}`)
	rec := &recordingAudit{}
	req := httptest.NewRequest("PATCH", "/ipam/dhcp/scope-templates/"+tid.String(), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountTemplates(f, rec).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !bytes.Contains(rec.calls[0].DiffJson, []byte(`"options":null`)) {
		t.Errorf("audit diff must contain \"options\":null, got %s", rec.calls[0].DiffJson)
	}
}

// The GET response must expose the column as "options" (Python
// parity) NOT "options_json" (the underlying dbq tag). Catches the
// raw-row leak that would break finch/operator tooling.
func TestGetDhcpScopeTemplate_ResponseUsesOptionsField(t *testing.T) {
	fid := uuid.New()
	tid := uuid.New()
	f := &dhcpTemplateFakeQ{
		getResult: dbq.DhcpScopeTemplate{
			ID: tid, FabricID: fid, Name: "tpl", IPFamily: 6,
			OptionsJSON: json.RawMessage(`[{"code":3,"data":"10.0.0.1"}]`),
		},
	}
	req := httptest.NewRequest("GET", "/ipam/dhcp/scope-templates/"+tid.String(), nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountTemplates(f, &recordingAudit{}).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.Bytes()
	if !bytes.Contains(body, []byte(`"options":[`)) {
		t.Errorf("response must expose `options`, got %s", body)
	}
	if bytes.Contains(body, []byte(`"options_json"`)) {
		t.Errorf("response must NOT expose `options_json` (dbq tag leak), got %s", body)
	}
}

func TestUpdateDhcpScopeTemplate_PreferredLifetimeOnV4Existing_400(t *testing.T) {
	fid := uuid.New()
	f := &dhcpTemplateFakeQ{
		getResult: dbq.DhcpScopeTemplate{ID: uuid.New(), FabricID: fid, IPFamily: 4},
	}
	preferred := int32(1000)
	body, _ := json.Marshal(map[string]any{"preferred_lifetime_seconds": preferred})
	req := httptest.NewRequest("PATCH", "/ipam/dhcp/scope-templates/"+uuid.New().String(), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountTemplates(f, &recordingAudit{}).ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestUpdateDhcpScopeTemplate_OptionsNullClearsToEmptyArray(t *testing.T) {
	fid := uuid.New()
	tid := uuid.New()
	f := &dhcpTemplateFakeQ{
		getResult: dbq.DhcpScopeTemplate{ID: tid, FabricID: fid, IPFamily: 6},
		updateResult: dbq.DhcpScopeTemplate{ID: tid, FabricID: fid, IPFamily: 6},
	}
	body := []byte(`{"options": null}`)
	req := httptest.NewRequest("PATCH", "/ipam/dhcp/scope-templates/"+tid.String(), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountTemplates(f, &recordingAudit{}).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !f.updateLast.OptionsSet {
		t.Errorf("OptionsSet must be true for \"options\":null")
	}
	if string(f.updateLast.OptionsJSON) != "[]" {
		t.Errorf("OptionsJSON = %s, want [] (Python parity: null clears to empty array)", f.updateLast.OptionsJSON)
	}
}

func TestUpdateDhcpScopeTemplate_AuditDiffOnlyContainsSetKeys(t *testing.T) {
	fid := uuid.New()
	tid := uuid.New()
	f := &dhcpTemplateFakeQ{
		getResult: dbq.DhcpScopeTemplate{ID: tid, FabricID: fid, IPFamily: 6},
		updateResult: dbq.DhcpScopeTemplate{ID: tid, FabricID: fid, IPFamily: 6},
	}
	body := []byte(`{"name": "new-name", "valid_lifetime_seconds": 7200}`)
	rec := &recordingAudit{}
	req := httptest.NewRequest("PATCH", "/ipam/dhcp/scope-templates/"+tid.String(), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountTemplates(f, rec).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("audit calls = %d", len(rec.calls))
	}
	var diff map[string]any
	if err := json.Unmarshal(rec.calls[0].DiffJson, &diff); err != nil {
		t.Fatal(err)
	}
	// Only the two set keys must appear — operators reading the
	// audit stream rely on absence ⇒ unchanged.
	for _, key := range []string{"name", "valid_lifetime_seconds"} {
		if _, ok := diff[key]; !ok {
			t.Errorf("diff missing %q: %v", key, diff)
		}
	}
	for _, key := range []string{"renew_timer_seconds", "rebind_timer_seconds", "description", "options"} {
		if _, ok := diff[key]; ok {
			t.Errorf("diff must NOT include unset key %q, got %v", key, diff[key])
		}
	}
}

// ---- DELETE ----

func TestDeleteDhcpScopeTemplate_HappyPath_204(t *testing.T) {
	fid := uuid.New()
	tid := uuid.New()
	f := &dhcpTemplateFakeQ{
		getResult: dbq.DhcpScopeTemplate{ID: tid, FabricID: fid, IPFamily: 4},
	}
	rec := &recordingAudit{}
	req := httptest.NewRequest("DELETE", "/ipam/dhcp/scope-templates/"+tid.String(), nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountTemplates(f, rec).ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", w.Code)
	}
	if f.deleteCount != 1 {
		t.Errorf("delete called %d times, want 1", f.deleteCount)
	}
	if len(rec.calls) != 1 || rec.calls[0].Action != "dhcp_scope_template.delete" {
		t.Errorf("audit: %+v", rec.calls)
	}
}

func TestDeleteDhcpScopeTemplate_NotFound_404(t *testing.T) {
	f := &dhcpTemplateFakeQ{getErr: pgx.ErrNoRows}
	req := httptest.NewRequest("DELETE", "/ipam/dhcp/scope-templates/"+uuid.New().String(), nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountTemplates(f, &recordingAudit{}).ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
