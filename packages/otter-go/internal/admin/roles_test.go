// PR 75 — handler tests for /admin/roles CRUD.
package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

// authedReq with specific capability set (overrides the wildcard
// principal that doReq uses). Tests no-escalation by limiting the
// caller to specific codes.
func authedReq(method, path string, body []byte, caps []string) *http.Request {
	var rdr *bytes.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	p := auth.Principal{Capabilities: caps}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	return req
}

// ---- list ----

func TestListRoles_HappyPath(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{
		rolesList:  []dbq.Role{{ID: id, Name: "viewer"}},
		rolesCount: 1,
	}
	rec := doReq(t, mount(f), "GET", "/admin/roles", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out listRolesResponse
	_ = json.NewDecoder(rec.Body).Decode(&out)
	if out.Total != 1 || len(out.Items) != 1 || out.Items[0].Name != "viewer" {
		t.Errorf("got %+v", out)
	}
}

func TestListRoles_RequiresReadCap(t *testing.T) {
	req := authedReq("GET", "/admin/roles", nil, []string{"admin:users:read"})
	rec := httptest.NewRecorder()
	mount(&fakeQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// ---- create ----

func TestCreateRole_HappyPath(t *testing.T) {
	f := &fakeQ{}
	body, _ := json.Marshal(map[string]any{
		"name":             "read-only",
		"permission_codes": []string{"ipam:vrfs:read", "ipam:subnets:read"},
	})
	rec := doReq(t, mount(f), "POST", "/admin/roles", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if f.gotRoleCreate.Name != "read-only" {
		t.Errorf("name = %q", f.gotRoleCreate.Name)
	}
}

func TestCreateRole_NoEscalationRejects(t *testing.T) {
	// Caller holds admin:roles:create but NOT ipam:vrfs:read —
	// attempting to grant that code should fail with 400.
	f := &fakeQ{}
	body, _ := json.Marshal(map[string]any{
		"name":             "wannabe-admin",
		"permission_codes": []string{"ipam:vrfs:read"},
	})
	req := authedReq("POST", "/admin/roles", body, []string{"admin:roles:create"})
	rec := httptest.NewRecorder()
	mount(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (no-escalation)", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("ipam:vrfs:read")) {
		t.Errorf("error body should name the missing code: %s", rec.Body.String())
	}
}

func TestCreateRole_WildcardCallerCanGrantAnything(t *testing.T) {
	// Caller with "*" can grant any code.
	f := &fakeQ{}
	body, _ := json.Marshal(map[string]any{
		"name":             "ipam-power-user",
		"permission_codes": []string{"ipam:vrfs:create", "ipam:subnets:delete"},
	})
	rec := doReq(t, mount(f), "POST", "/admin/roles", body)
	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201 (wildcard caller)", rec.Code)
	}
}

func TestCreateRole_NamespaceWildcardGrantsScopedCodes(t *testing.T) {
	// Caller holds "ipam:*" — can grant any ipam:* code, but
	// nothing else.
	f := &fakeQ{}
	body, _ := json.Marshal(map[string]any{
		"name":             "ipam-only",
		"permission_codes": []string{"ipam:vrfs:read", "ipam:subnets:create"},
	})
	req := authedReq("POST", "/admin/roles", body,
		[]string{"admin:roles:create", "ipam:*:*"})
	rec := httptest.NewRecorder()
	mount(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201 (ipam:*:* should grant ipam codes)", rec.Code)
	}
}

func TestCreateRole_DuplicateNameIs409(t *testing.T) {
	existing := dbq.Role{ID: uuid.New(), Name: "dup"}
	f := &fakeQ{roleByName: map[string]dbq.Role{"dup": existing}}
	body, _ := json.Marshal(map[string]any{"name": "dup", "permission_codes": []string{}})
	rec := doReq(t, mount(f), "POST", "/admin/roles", body)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
}

func TestCreateRole_MissingNameIs400(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"permission_codes": []string{}})
	rec := doReq(t, mount(&fakeQ{}), "POST", "/admin/roles", body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestCreateRole_RequiresCreateCap(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"name": "x", "permission_codes": []string{}})
	req := authedReq("POST", "/admin/roles", body, []string{"admin:roles:read"})
	rec := httptest.NewRecorder()
	mount(&fakeQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// ---- update ----

func TestUpdateRole_HappyPath(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{
		roleByID: map[uuid.UUID]dbq.Role{id: {ID: id, Name: "viewer", IsSystem: false}},
	}
	body, _ := json.Marshal(map[string]any{"description": "Read-only access"})
	rec := doReq(t, mount(f), "PATCH", "/admin/roles/"+id.String(), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if !f.gotRoleUpdate.DescriptionSet {
		t.Errorf("DescriptionSet should be true")
	}
}

func TestUpdateRole_SystemRoleIs400(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{
		roleByID: map[uuid.UUID]dbq.Role{id: {ID: id, Name: "system-admin", IsSystem: true}},
	}
	body, _ := json.Marshal(map[string]any{"description": "trying to edit"})
	rec := doReq(t, mount(f), "PATCH", "/admin/roles/"+id.String(), body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (system role)", rec.Code)
	}
}

func TestUpdateRole_NotFoundIs404(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{} // empty roleByID → ErrNoRows
	body, _ := json.Marshal(map[string]any{"description": "x"})
	rec := doReq(t, mount(f), "PATCH", "/admin/roles/"+id.String(), body)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestUpdateRole_NoEscalationAppliesToPermissionUpdate(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{
		roleByID: map[uuid.UUID]dbq.Role{id: {ID: id, IsSystem: false}},
	}
	body, _ := json.Marshal(map[string]any{
		"permission_codes": []string{"dns:zones:create"},
	})
	// Caller holds admin:roles:update but not dns:zones:create.
	req := authedReq("PATCH", "/admin/roles/"+id.String(), body,
		[]string{"admin:roles:update"})
	rec := httptest.NewRecorder()
	mount(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (no-escalation on update)", rec.Code)
	}
}

func TestUpdateRole_OmittedFieldsLeaveOthersUntouched(t *testing.T) {
	// Empty PATCH body (no description, no permission_codes) →
	// both *Set flags false, SQL preserves both columns.
	id := uuid.New()
	f := &fakeQ{
		roleByID: map[uuid.UUID]dbq.Role{id: {ID: id, IsSystem: false}},
	}
	rec := doReq(t, mount(f), "PATCH", "/admin/roles/"+id.String(), []byte(`{}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if f.gotRoleUpdate.DescriptionSet || f.gotRoleUpdate.PermissionCodesSet {
		t.Errorf("empty PATCH should leave both *Set flags false: %+v", f.gotRoleUpdate)
	}
}

func TestUpdateRole_BadUUIDIs400(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"description": "x"})
	rec := doReq(t, mount(&fakeQ{}), "PATCH", "/admin/roles/not-a-uuid", body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// ---- delete ----

func TestDeleteRole_HappyPath(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{
		roleByID:       map[uuid.UUID]dbq.Role{id: {ID: id, IsSystem: false}},
		deleteRoleRows: 1,
	}
	rec := doReq(t, mount(f), "DELETE", "/admin/roles/"+id.String(), nil)
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestDeleteRole_SystemRoleIs400(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{roleByID: map[uuid.UUID]dbq.Role{id: {ID: id, IsSystem: true}}}
	rec := doReq(t, mount(f), "DELETE", "/admin/roles/"+id.String(), nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (system role)", rec.Code)
	}
}

func TestDeleteRole_AssignedRoleIs409(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{
		roleByID:          map[uuid.UUID]dbq.Role{id: {ID: id, IsSystem: false}},
		roleAssignedCount: 3,
	}
	rec := doReq(t, mount(f), "DELETE", "/admin/roles/"+id.String(), nil)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (role still assigned)", rec.Code)
	}
}

func TestDeleteRole_NotFoundIs404(t *testing.T) {
	id := uuid.New()
	rec := doReq(t, mount(&fakeQ{}), "DELETE", "/admin/roles/"+id.String(), nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestDeleteRole_LostRaceIs404(t *testing.T) {
	// Pre-checks pass but the DELETE returns 0 rows (someone else
	// deleted or flipped is_system between calls). Should 404.
	id := uuid.New()
	f := &fakeQ{
		roleByID:       map[uuid.UUID]dbq.Role{id: {ID: id, IsSystem: false}},
		deleteRoleRows: 0,
	}
	rec := doReq(t, mount(f), "DELETE", "/admin/roles/"+id.String(), nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (lost race)", rec.Code)
	}
}

func TestDeleteRole_BadUUIDIs400(t *testing.T) {
	rec := doReq(t, mount(&fakeQ{}), "DELETE", "/admin/roles/not-a-uuid", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
