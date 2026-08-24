// PR 77 — /admin/oidc-role-mappings CRUD.
//
// Maps IdP role claims (Keycloak / other OIDC providers) to DCIM
// roles + optional ABAC scope. The login flow joins on idp_role to
// materialize a Principal's capability set; the admin surface
// lets operators wire new IdP roles without code changes.
//
// scope_dimension reuses the scope_type pg enum but excludes
// "global" (a mapping with no dimension IS global). Empty / NULL /
// "global" on the wire all normalize to NULL in the DB.
package admin

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// validOidcScopeDimensions: the scope_type enum minus "global"
// (which means "no dimension" → NULL).
var validOidcScopeDimensions = map[string]struct{}{
	"region": {}, "site": {}, "site_group": {},
	"enclave": {}, "organization": {}, "fabric": {}, "classification": {},
}

// normalizeScopeDimension coerces the wire string to the DB value:
// nil, "", or "global" → NULL. Anything else must be in the valid
// set, else the caller returns 400.
func normalizeScopeDimension(v *string) (*string, bool) {
	if v == nil || *v == "" || *v == "global" {
		return nil, true
	}
	if _, ok := validOidcScopeDimensions[*v]; !ok {
		return nil, false
	}
	return v, true
}

type oidcMappingOut struct {
	ID             uuid.UUID `json:"id"`
	IdpRole        string    `json:"idp_role"`
	ClaimSource    string    `json:"claim_source"`
	DcimRoleID     uuid.UUID `json:"dcim_role_id"`
	DcimRoleName   string    `json:"dcim_role_name"`
	Description    *string   `json:"description"`
	ScopeDimension *string   `json:"scope_dimension"`
	ScopeTarget    *string   `json:"scope_target"`
	CreatedAt      string    `json:"created_at"`
}

// hydrateMappings joins N OIDC mapping rows with their dcim_role_name
// in two round-trips total (rows + role-name lookup). Matches the
// pattern used by listUserAssignments.
func (h *Handler) hydrateMappings(
	r *http.Request, rows []dbq.OidcRoleMapping,
) ([]oidcMappingOut, error) {
	if len(rows) == 0 {
		return []oidcMappingOut{}, nil
	}
	roleIDs := make([]uuid.UUID, 0, len(rows))
	for _, m := range rows {
		roleIDs = append(roleIDs, m.DcimRoleID)
	}
	names, err := h.Q.GetRoleNamesByIDs(r.Context(), roleIDs)
	if err != nil {
		return nil, err
	}
	nameByID := make(map[uuid.UUID]string, len(names))
	for _, n := range names {
		nameByID[n.ID] = n.Name
	}
	out := make([]oidcMappingOut, len(rows))
	for i, m := range rows {
		name := nameByID[m.DcimRoleID]
		if name == "" {
			name = "(unknown)"
		}
		out[i] = oidcMappingOut{
			ID: m.ID, IdpRole: m.IdpRole, ClaimSource: m.ClaimSource,
			DcimRoleID: m.DcimRoleID, DcimRoleName: name,
			Description: m.Description, ScopeDimension: m.ScopeDimension,
			ScopeTarget: m.ScopeTarget,
			CreatedAt:   m.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		}
	}
	return out, nil
}

// ---- list ----

type listOidcMappingsResponse struct {
	Items  []oidcMappingOut `json:"items"`
	Total  int64            `json:"total"`
	Limit  int32            `json:"limit"`
	Offset int32            `json:"offset"`
}

func (h *Handler) listOidcMappings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	rows, err := h.Q.ListOidcRoleMappings(r.Context(), dbq.ListOidcRoleMappingsParams{
		Limit: limit, Offset: offset,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountOidcRoleMappings(r.Context())
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	items, err := h.hydrateMappings(r, rows)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, listOidcMappingsResponse{
		Items: items, Total: total, Limit: limit, Offset: offset,
	})
}

// ---- create ----

type oidcMappingCreateReq struct {
	IdpRole        string    `json:"idp_role"`
	ClaimSource    string    `json:"claim_source"`
	DcimRoleID     uuid.UUID `json:"dcim_role_id"`
	Description    *string   `json:"description"`
	ScopeDimension *string   `json:"scope_dimension"`
	ScopeTarget    *string   `json:"scope_target"`
}

func (h *Handler) createOidcMapping(w http.ResponseWriter, r *http.Request) {
	var req oidcMappingCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.IdpRole == "" || req.DcimRoleID == uuid.Nil {
		httpx.Error(w, http.StatusBadRequest, "idp_role and dcim_role_id required")
		return
	}
	dim, ok := normalizeScopeDimension(req.ScopeDimension)
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "unknown scope_dimension")
		return
	}
	// Target without a dimension is meaningless — null it out for
	// shape consistency.
	target := req.ScopeTarget
	if dim == nil {
		target = nil
	}
	// Verify the target DCIM role exists.
	if _, err := h.Q.GetAdminRole(r.Context(), req.DcimRoleID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "role not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	// Pre-check idp_role uniqueness (column has UNIQUE constraint
	// but pre-check gives a clean 409).
	if _, err := h.Q.GetOidcRoleMappingByIdpRole(r.Context(), req.IdpRole); err == nil {
		httpx.Error(w, http.StatusConflict,
			"a mapping for that IdP role already exists")
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	claim := req.ClaimSource
	if claim == "" {
		claim = "keycloak"
	}
	out, err := h.Q.CreateOidcRoleMapping(r.Context(), dbq.CreateOidcRoleMappingParams{
		IdpRole: req.IdpRole, ClaimSource: claim, DcimRoleID: req.DcimRoleID,
		Description: req.Description, ScopeDimension: dim, ScopeTarget: target,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action:     "oidc_role_mapping.create",
		TargetType: "oidc_role_mapping",
		TargetID:   out.ID.String(),
	})
	hydrated, err := h.hydrateMappings(r, []dbq.OidcRoleMapping{out})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusCreated, hydrated[0])
}

// ---- update ----

type oidcMappingUpdateReq struct {
	ClaimSource       *string
	DcimRoleID        *uuid.UUID
	Description       *string
	descriptionSet    bool
	ScopeDimension    *string
	scopeDimensionSet bool
	ScopeTarget       *string
	scopeTargetSet    bool
}

func (u *oidcMappingUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["claim_source"]; ok {
		_ = json.Unmarshal(v, &u.ClaimSource)
	}
	if v, ok := raw["dcim_role_id"]; ok {
		_ = json.Unmarshal(v, &u.DcimRoleID)
	}
	if v, ok := raw["description"]; ok {
		u.descriptionSet = true
		_ = json.Unmarshal(v, &u.Description)
	}
	if v, ok := raw["scope_dimension"]; ok {
		u.scopeDimensionSet = true
		_ = json.Unmarshal(v, &u.ScopeDimension)
	}
	if v, ok := raw["scope_target"]; ok {
		u.scopeTargetSet = true
		_ = json.Unmarshal(v, &u.ScopeTarget)
	}
	return nil
}

func (h *Handler) updateOidcMapping(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	var req oidcMappingUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	// Validate scope_dimension if it was set on the wire (incl. nil).
	var dim *string
	if req.scopeDimensionSet {
		d, ok := normalizeScopeDimension(req.ScopeDimension)
		if !ok {
			httpx.Error(w, http.StatusBadRequest, "unknown scope_dimension")
			return
		}
		dim = d
	}
	// If dcim_role_id is changing, verify the target role exists.
	if req.DcimRoleID != nil {
		if _, err := h.Q.GetAdminRole(r.Context(), *req.DcimRoleID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httpx.Error(w, http.StatusNotFound, "role not found")
				return
			}
			status, msg := httpx.Mapped(err)
			httpx.Error(w, status, msg)
			return
		}
	}
	out, err := h.Q.UpdateOidcRoleMapping(r.Context(), dbq.UpdateOidcRoleMappingParams{
		ID:             id,
		ClaimSource:    req.ClaimSource,
		DcimRoleID:     req.DcimRoleID,
		DescriptionSet: req.descriptionSet,
		Description:    req.Description,
		ScopeDimSet:    req.scopeDimensionSet,
		ScopeDimension: dim,
		ScopeTargetSet: req.scopeTargetSet,
		ScopeTarget:    req.ScopeTarget,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "mapping not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action:     "oidc_role_mapping.update",
		TargetType: "oidc_role_mapping",
		TargetID:   id.String(),
	})
	hydrated, err := h.hydrateMappings(r, []dbq.OidcRoleMapping{out})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, hydrated[0])
}

// ---- delete ----

func (h *Handler) deleteOidcMapping(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	n, err := h.Q.DeleteOidcRoleMapping(r.Context(), id)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if n == 0 {
		httpx.Error(w, http.StatusNotFound, "mapping not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action:     "oidc_role_mapping.delete",
		TargetType: "oidc_role_mapping",
		TargetID:   id.String(),
	})
	w.WriteHeader(http.StatusNoContent)
}
