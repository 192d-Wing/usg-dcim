// PR 76 — handler tests for assignments.
package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// ---- list per user ----

func TestListUserAssignments_HappyPath(t *testing.T) {
	userID := uuid.New()
	roleID := uuid.New()
	assignmentID := uuid.New()
	f := &fakeQ{
		assignments: []dbq.UserRoleRow{{ID: assignmentID, UserID: userID, RoleID: roleID}},
		roleNamesByID: map[uuid.UUID]string{roleID: "viewer"},
		scopesByAssignment: []dbq.RoleScopeRow{
			{ID: uuid.New(), AssignmentID: assignmentID, ScopeType: "site"},
		},
	}
	rec := doReq(t, mount(f), "GET", "/admin/users/"+userID.String()+"/assignments", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var out []assignmentOut
	_ = json.NewDecoder(rec.Body).Decode(&out)
	if len(out) != 1 {
		t.Fatalf("got %d assignments, want 1", len(out))
	}
	if out[0].RoleName != "viewer" {
		t.Errorf("role_name = %q", out[0].RoleName)
	}
	if len(out[0].Scopes) != 1 || out[0].Scopes[0].ScopeType != "site" {
		t.Errorf("scopes = %+v", out[0].Scopes)
	}
}

func TestListUserAssignments_UnknownRoleFallback(t *testing.T) {
	// Role row missing → role_name = "(unknown)" matches Python.
	userID := uuid.New()
	f := &fakeQ{
		assignments:   []dbq.UserRoleRow{{ID: uuid.New(), UserID: userID, RoleID: uuid.New()}},
		roleNamesByID: map[uuid.UUID]string{},
	}
	rec := doReq(t, mount(f), "GET", "/admin/users/"+userID.String()+"/assignments", nil)
	var out []assignmentOut
	_ = json.NewDecoder(rec.Body).Decode(&out)
	if out[0].RoleName != "(unknown)" {
		t.Errorf("role_name = %q, want (unknown)", out[0].RoleName)
	}
}

func TestListUserAssignments_UserNotFoundIs404(t *testing.T) {
	f := &fakeQ{getErr: pgx.ErrNoRows}
	rec := doReq(t, mount(f), "GET", "/admin/users/"+uuid.New().String()+"/assignments", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestListUserAssignments_BadUUIDIs400(t *testing.T) {
	rec := doReq(t, mount(&fakeQ{}), "GET", "/admin/users/not-a-uuid/assignments", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestListUserAssignments_EmptyArrayForUserWithoutRoles(t *testing.T) {
	userID := uuid.New()
	f := &fakeQ{assignments: nil}
	rec := doReq(t, mount(f), "GET", "/admin/users/"+userID.String()+"/assignments", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("[]")) {
		t.Errorf("expected empty array, got %s", rec.Body.String())
	}
}

// ---- create ----

func TestCreateAssignment_HappyPath(t *testing.T) {
	userID := uuid.New()
	roleID := uuid.New()
	f := &fakeQ{
		roleByID:      map[uuid.UUID]dbq.Role{roleID: {ID: roleID, Name: "viewer"}},
		roleNamesByID: map[uuid.UUID]string{roleID: "viewer"},
	}
	body, _ := json.Marshal(map[string]any{
		"user_id": userID, "role_id": roleID,
		"scopes": []map[string]any{
			{"scope_type": "site", "target_id": uuid.New().String()},
			{"scope_type": "global"},
		},
	})
	rec := doReq(t, mount(f), "POST", "/admin/assignments", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if len(f.gotScopeCreates) != 2 {
		t.Errorf("got %d scope creates, want 2", len(f.gotScopeCreates))
	}
	if f.gotAssignmentCreate != [2]uuid.UUID{userID, roleID} {
		t.Errorf("forwarded ids wrong: %v", f.gotAssignmentCreate)
	}
}

func TestCreateAssignment_NoScopesIsValid(t *testing.T) {
	// scopes=[] is acceptable — implies global role grant.
	userID := uuid.New()
	roleID := uuid.New()
	f := &fakeQ{
		roleByID:      map[uuid.UUID]dbq.Role{roleID: {ID: roleID, Name: "global"}},
		roleNamesByID: map[uuid.UUID]string{roleID: "global"},
	}
	body, _ := json.Marshal(map[string]any{"user_id": userID, "role_id": roleID})
	rec := doReq(t, mount(f), "POST", "/admin/assignments", body)
	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if len(f.gotScopeCreates) != 0 {
		t.Errorf("got %d scope creates, want 0", len(f.gotScopeCreates))
	}
}

func TestCreateAssignment_UnknownScopeTypeIs400(t *testing.T) {
	userID := uuid.New()
	roleID := uuid.New()
	body, _ := json.Marshal(map[string]any{
		"user_id": userID, "role_id": roleID,
		"scopes": []map[string]any{{"scope_type": "weird"}},
	})
	rec := doReq(t, mount(&fakeQ{}), "POST", "/admin/assignments", body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (unknown scope_type)", rec.Code)
	}
}

func TestCreateAssignment_UserNotFoundIs404(t *testing.T) {
	roleID := uuid.New()
	f := &fakeQ{
		getErr:   pgx.ErrNoRows, // user lookup fails
		roleByID: map[uuid.UUID]dbq.Role{roleID: {ID: roleID}},
	}
	body, _ := json.Marshal(map[string]any{"user_id": uuid.New(), "role_id": roleID})
	rec := doReq(t, mount(f), "POST", "/admin/assignments", body)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (user)", rec.Code)
	}
}

func TestCreateAssignment_RoleNotFoundIs404(t *testing.T) {
	// User lookup succeeds (default zero), role lookup fails (empty map).
	f := &fakeQ{} // no roleByID entry
	body, _ := json.Marshal(map[string]any{"user_id": uuid.New(), "role_id": uuid.New()})
	rec := doReq(t, mount(f), "POST", "/admin/assignments", body)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (role)", rec.Code)
	}
}

func TestCreateAssignment_DuplicateIs409(t *testing.T) {
	userID, roleID := uuid.New(), uuid.New()
	existing := dbq.UserRoleRow{ID: uuid.New(), UserID: userID, RoleID: roleID}
	f := &fakeQ{
		roleByID:      map[uuid.UUID]dbq.Role{roleID: {ID: roleID}},
		dupAssignment: map[[2]uuid.UUID]dbq.UserRoleRow{{userID, roleID}: existing},
	}
	body, _ := json.Marshal(map[string]any{"user_id": userID, "role_id": roleID})
	rec := doReq(t, mount(f), "POST", "/admin/assignments", body)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
}

func TestCreateAssignment_MissingFieldsIs400(t *testing.T) {
	rec := doReq(t, mount(&fakeQ{}), "POST", "/admin/assignments", []byte(`{}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestCreateAssignment_RequiresUpdateCap(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"user_id": uuid.New(), "role_id": uuid.New()})
	req := authedReq("POST", "/admin/assignments", body, []string{"admin:users:read"})
	rec := httptest.NewRecorder()
	mount(&fakeQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// ---- delete ----

func TestDeleteAssignment_HappyPath(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{
		assignmentByID: map[uuid.UUID]dbq.UserRoleRow{id: {ID: id}},
	}
	rec := doReq(t, mount(f), "DELETE", "/admin/assignments/"+id.String(), nil)
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestDeleteAssignment_NotFoundIs404(t *testing.T) {
	rec := doReq(t, mount(&fakeQ{}), "DELETE", "/admin/assignments/"+uuid.New().String(), nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestDeleteAssignment_BadUUIDIs400(t *testing.T) {
	rec := doReq(t, mount(&fakeQ{}), "DELETE", "/admin/assignments/not-a-uuid", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestDeleteAssignment_RequiresUpdateCap(t *testing.T) {
	req := authedReq("DELETE", "/admin/assignments/"+uuid.New().String(), nil,
		[]string{"admin:users:read"})
	rec := httptest.NewRecorder()
	mount(&fakeQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}
