// PR 77 — handler tests for /admin/oidc-role-mappings.
package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// ---- list ----

func TestListOidcMappings_HappyPath(t *testing.T) {
	roleID := uuid.New()
	f := &fakeQ{
		oidcList: []dbq.OidcRoleMapping{
			{ID: uuid.New(), IdpRole: "keycloak-admin", ClaimSource: "keycloak",
				DcimRoleID: roleID, CreatedAt: time.Now()},
		},
		oidcCount:     1,
		roleNamesByID: map[uuid.UUID]string{roleID: "admin"},
	}
	rec := doReq(t, mount(f), "GET", "/admin/oidc-role-mappings", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var out listOidcMappingsResponse
	_ = json.NewDecoder(rec.Body).Decode(&out)
	if out.Total != 1 || len(out.Items) != 1 {
		t.Fatalf("got %+v", out)
	}
	if out.Items[0].DcimRoleName != "admin" {
		t.Errorf("dcim_role_name = %q", out.Items[0].DcimRoleName)
	}
}

func TestListOidcMappings_UnknownRoleFallback(t *testing.T) {
	// Stale dcim_role_id (role was deleted) → fallback name.
	f := &fakeQ{
		oidcList: []dbq.OidcRoleMapping{
			{ID: uuid.New(), IdpRole: "x", DcimRoleID: uuid.New(), CreatedAt: time.Now()},
		},
		oidcCount: 1,
	}
	rec := doReq(t, mount(f), "GET", "/admin/oidc-role-mappings", nil)
	var out listOidcMappingsResponse
	_ = json.NewDecoder(rec.Body).Decode(&out)
	if out.Items[0].DcimRoleName != "(unknown)" {
		t.Errorf("dcim_role_name = %q, want (unknown)", out.Items[0].DcimRoleName)
	}
}

func TestListOidcMappings_RequiresReadCap(t *testing.T) {
	req := authedReq("GET", "/admin/oidc-role-mappings", nil,
		[]string{"admin:users:read"})
	rec := httptest.NewRecorder()
	mount(&fakeQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// ---- create ----

func TestCreateOidcMapping_HappyPath(t *testing.T) {
	roleID := uuid.New()
	f := &fakeQ{
		roleByID:      map[uuid.UUID]dbq.Role{roleID: {ID: roleID, Name: "admin"}},
		roleNamesByID: map[uuid.UUID]string{roleID: "admin"},
	}
	body, _ := json.Marshal(map[string]any{
		"idp_role":     "keycloak-admin",
		"dcim_role_id": roleID,
	})
	rec := doReq(t, mount(f), "POST", "/admin/oidc-role-mappings", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	// Default claim_source when caller omits.
	if f.gotOidcCreate.ClaimSource != "keycloak" {
		t.Errorf("claim_source = %q, want keycloak (default)", f.gotOidcCreate.ClaimSource)
	}
}

func TestCreateOidcMapping_ExplicitClaimSource(t *testing.T) {
	roleID := uuid.New()
	f := &fakeQ{
		roleByID:      map[uuid.UUID]dbq.Role{roleID: {ID: roleID, Name: "admin"}},
		roleNamesByID: map[uuid.UUID]string{roleID: "admin"},
	}
	body, _ := json.Marshal(map[string]any{
		"idp_role":     "okta-admin",
		"claim_source": "okta",
		"dcim_role_id": roleID,
	})
	doReq(t, mount(f), "POST", "/admin/oidc-role-mappings", body)
	if f.gotOidcCreate.ClaimSource != "okta" {
		t.Errorf("claim_source = %q, want okta", f.gotOidcCreate.ClaimSource)
	}
}

func TestCreateOidcMapping_ScopeDimensionAndTarget(t *testing.T) {
	roleID := uuid.New()
	siteID := uuid.New().String()
	f := &fakeQ{
		roleByID:      map[uuid.UUID]dbq.Role{roleID: {ID: roleID, Name: "site-admin"}},
		roleNamesByID: map[uuid.UUID]string{roleID: "site-admin"},
	}
	body, _ := json.Marshal(map[string]any{
		"idp_role":        "kc-site-admin",
		"dcim_role_id":    roleID,
		"scope_dimension": "site",
		"scope_target":    siteID,
	})
	rec := doReq(t, mount(f), "POST", "/admin/oidc-role-mappings", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d", rec.Code)
	}
	if f.gotOidcCreate.ScopeDimension == nil || *f.gotOidcCreate.ScopeDimension != "site" {
		t.Errorf("scope_dimension = %v, want site", f.gotOidcCreate.ScopeDimension)
	}
	if f.gotOidcCreate.ScopeTarget == nil || *f.gotOidcCreate.ScopeTarget != siteID {
		t.Errorf("scope_target = %v, want %q", f.gotOidcCreate.ScopeTarget, siteID)
	}
}

func TestCreateOidcMapping_GlobalScopeNormalizesToNull(t *testing.T) {
	// scope_dimension="global" → DB stores NULL (matches Python's
	// _validate_scope_dimension which returns None for "global").
	roleID := uuid.New()
	f := &fakeQ{
		roleByID:      map[uuid.UUID]dbq.Role{roleID: {ID: roleID, Name: "admin"}},
		roleNamesByID: map[uuid.UUID]string{roleID: "admin"},
	}
	body, _ := json.Marshal(map[string]any{
		"idp_role":        "x",
		"dcim_role_id":    roleID,
		"scope_dimension": "global",
		"scope_target":    "some-target",
	})
	doReq(t, mount(f), "POST", "/admin/oidc-role-mappings", body)
	if f.gotOidcCreate.ScopeDimension != nil {
		t.Errorf("scope_dimension = %v, want nil for 'global'", f.gotOidcCreate.ScopeDimension)
	}
	// Target should also be cleared when dimension is null.
	if f.gotOidcCreate.ScopeTarget != nil {
		t.Errorf("scope_target = %v, want nil when dimension is null", f.gotOidcCreate.ScopeTarget)
	}
}

func TestCreateOidcMapping_UnknownScopeDimensionIs400(t *testing.T) {
	roleID := uuid.New()
	body, _ := json.Marshal(map[string]any{
		"idp_role": "x", "dcim_role_id": roleID, "scope_dimension": "weird",
	})
	rec := doReq(t, mount(&fakeQ{}), "POST", "/admin/oidc-role-mappings", body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestCreateOidcMapping_RoleNotFoundIs404(t *testing.T) {
	f := &fakeQ{} // empty roleByID
	body, _ := json.Marshal(map[string]any{"idp_role": "x", "dcim_role_id": uuid.New()})
	rec := doReq(t, mount(f), "POST", "/admin/oidc-role-mappings", body)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestCreateOidcMapping_DuplicateIdpRoleIs409(t *testing.T) {
	roleID := uuid.New()
	f := &fakeQ{
		roleByID: map[uuid.UUID]dbq.Role{roleID: {ID: roleID}},
		oidcByIdpRole: map[string]dbq.OidcRoleMapping{
			"dup": {ID: uuid.New(), IdpRole: "dup"},
		},
	}
	body, _ := json.Marshal(map[string]any{"idp_role": "dup", "dcim_role_id": roleID})
	rec := doReq(t, mount(f), "POST", "/admin/oidc-role-mappings", body)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
}

func TestCreateOidcMapping_MissingFieldsIs400(t *testing.T) {
	rec := doReq(t, mount(&fakeQ{}), "POST", "/admin/oidc-role-mappings", []byte(`{}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestCreateOidcMapping_RequiresCreateCap(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"idp_role": "x", "dcim_role_id": uuid.New()})
	req := authedReq("POST", "/admin/oidc-role-mappings", body,
		[]string{"admin:oidc-mappings:read"})
	rec := httptest.NewRecorder()
	mount(&fakeQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// ---- update ----

func TestUpdateOidcMapping_HappyPath(t *testing.T) {
	id := uuid.New()
	roleID := uuid.New()
	f := &fakeQ{
		roleByID:      map[uuid.UUID]dbq.Role{roleID: {ID: roleID, Name: "admin"}},
		roleNamesByID: map[uuid.UUID]string{roleID: "admin"},
		oidcUpdateOut: dbq.OidcRoleMapping{ID: id, DcimRoleID: roleID},
	}
	body, _ := json.Marshal(map[string]any{"description": "Keycloak admin role"})
	rec := doReq(t, mount(f), "PATCH", "/admin/oidc-role-mappings/"+id.String(), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if !f.gotOidcUpdate.DescriptionSet {
		t.Errorf("DescriptionSet should be true")
	}
}

func TestUpdateOidcMapping_RoleNotFoundIs404(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{} // role lookup fails
	body, _ := json.Marshal(map[string]any{"dcim_role_id": uuid.New()})
	rec := doReq(t, mount(f), "PATCH", "/admin/oidc-role-mappings/"+id.String(), body)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (target role)", rec.Code)
	}
}

func TestUpdateOidcMapping_UnknownScopeDimensionIs400(t *testing.T) {
	id := uuid.New()
	body, _ := json.Marshal(map[string]any{"scope_dimension": "weird"})
	rec := doReq(t, mount(&fakeQ{}), "PATCH", "/admin/oidc-role-mappings/"+id.String(), body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestUpdateOidcMapping_NotFoundIs404(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{oidcUpdateErr: pgx.ErrNoRows}
	body, _ := json.Marshal(map[string]any{"description": "x"})
	rec := doReq(t, mount(f), "PATCH", "/admin/oidc-role-mappings/"+id.String(), body)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestUpdateOidcMapping_BadUUIDIs400(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"description": "x"})
	rec := doReq(t, mount(&fakeQ{}), "PATCH", "/admin/oidc-role-mappings/not-a-uuid", body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestUpdateOidcMapping_NullScopeDimensionClearsTarget(t *testing.T) {
	// Setting scope_dimension to null/global should imply target=null
	// even if the caller omitted it. SQL CASE in update query
	// handles this; the handler forwards the *Set flag.
	id := uuid.New()
	f := &fakeQ{oidcUpdateOut: dbq.OidcRoleMapping{ID: id}}
	body := []byte(`{"scope_dimension":null}`)
	rec := doReq(t, mount(f), "PATCH", "/admin/oidc-role-mappings/"+id.String(), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !f.gotOidcUpdate.ScopeDimSet {
		t.Errorf("ScopeDimSet should be true for explicit-null")
	}
	if f.gotOidcUpdate.ScopeDimension != nil {
		t.Errorf("ScopeDimension = %v, want nil", f.gotOidcUpdate.ScopeDimension)
	}
}

func TestUpdateOidcMapping_RequiresUpdateCap(t *testing.T) {
	id := uuid.New()
	body, _ := json.Marshal(map[string]any{"description": "x"})
	req := authedReq("PATCH", "/admin/oidc-role-mappings/"+id.String(), body,
		[]string{"admin:oidc-mappings:read"})
	rec := httptest.NewRecorder()
	mount(&fakeQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// ---- delete ----

func TestDeleteOidcMapping_HappyPath(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{oidcDeleteRows: 1}
	rec := doReq(t, mount(f), "DELETE", "/admin/oidc-role-mappings/"+id.String(), nil)
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestDeleteOidcMapping_NotFoundIs404(t *testing.T) {
	id := uuid.New()
	rec := doReq(t, mount(&fakeQ{oidcDeleteRows: 0}),
		"DELETE", "/admin/oidc-role-mappings/"+id.String(), nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestDeleteOidcMapping_BadUUIDIs400(t *testing.T) {
	rec := doReq(t, mount(&fakeQ{}), "DELETE", "/admin/oidc-role-mappings/not-a-uuid", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestDeleteOidcMapping_RequiresDeleteCap(t *testing.T) {
	id := uuid.New()
	req := authedReq("DELETE", "/admin/oidc-role-mappings/"+id.String(), nil,
		[]string{"admin:oidc-mappings:read"})
	rec := httptest.NewRecorder()
	mount(&fakeQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// Tests that don't fit neatly elsewhere — keeps test order stable
// even with the bytes import (used by TestListOidcMappings... if needed).
var _ = bytes.NewReader
