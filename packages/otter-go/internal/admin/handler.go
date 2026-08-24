// Package admin holds /api/v1/admin endpoints — user/role/
// assignment/OIDC-mapping CRUD. The login-path reads (OIDC sign-in,
// API tokens, capability resolution) live in internal/auth; this
// package is operator-only and gates every route on admin:* caps.
//
// PR 74: Users CRUD (list / create / update). Roles + assignments
// + OIDC mappings land in follow-up PRs.
//
// Every mutation emits an audit row. Admin actions have stricter
// retention requirements than regular CRUD — the audit_log table
// is the only operator-visible trail of who created whom.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type Querier interface {
	ListAdminUsers(ctx context.Context, arg dbq.ListAdminUsersParams) ([]dbq.ListAdminUsersRow, error)
	CountAdminUsers(ctx context.Context) (int64, error)
	GetUser(ctx context.Context, id uuid.UUID) (dbq.User, error)
	GetUserByEmail(ctx context.Context, email string) (dbq.User, error)
	CreateAdminUser(ctx context.Context, arg dbq.CreateAdminUserParams) (dbq.CreateAdminUserRow, error)
	UpdateAdminUser(ctx context.Context, arg dbq.UpdateAdminUserParams) (dbq.UpdateAdminUserRow, error)
	SetUserPasswordHash(ctx context.Context, arg dbq.SetUserPasswordHashParams) (int64, error)

	ListAdminRoles(ctx context.Context, arg dbq.ListAdminRolesParams) ([]dbq.Role, error)
	CountAdminRoles(ctx context.Context) (int64, error)
	GetAdminRole(ctx context.Context, id uuid.UUID) (dbq.Role, error)
	GetAdminRoleByName(ctx context.Context, name string) (dbq.Role, error)
	CreateAdminRole(ctx context.Context, arg dbq.CreateAdminRoleParams) (dbq.Role, error)
	UpdateAdminRole(ctx context.Context, arg dbq.UpdateAdminRoleParams) (dbq.Role, error)
	DeleteAdminRole(ctx context.Context, id uuid.UUID) (int64, error)
	CountUserRolesForRole(ctx context.Context, roleID uuid.UUID) (int64, error)

	ListUserAssignments(ctx context.Context, userID uuid.UUID) ([]dbq.UserRole, error)
	GetUserRole(ctx context.Context, id uuid.UUID) (dbq.UserRole, error)
	FindUserRoleByUserAndRole(ctx context.Context, arg dbq.FindUserRoleByUserAndRoleParams) (dbq.UserRole, error)
	CreateUserRole(ctx context.Context, arg dbq.CreateUserRoleParams) (dbq.UserRole, error)
	DeleteUserRole(ctx context.Context, id uuid.UUID) (int64, error)
	ListRoleScopesByAssignment(ctx context.Context, assignmentID uuid.UUID) ([]dbq.RoleScope, error)
	ListRoleScopesByAssignments(ctx context.Context, ids []uuid.UUID) ([]dbq.RoleScope, error)
	CreateRoleScope(ctx context.Context, arg dbq.CreateRoleScopeParams) (dbq.RoleScope, error)
	DeleteRoleScopesForAssignment(ctx context.Context, assignmentID uuid.UUID) error
	GetRoleNamesByIDs(ctx context.Context, ids []uuid.UUID) ([]dbq.GetRoleNamesByIDsRow, error)

	ListOidcRoleMappings(ctx context.Context, arg dbq.ListOidcRoleMappingsParams) ([]dbq.OidcRoleMapping, error)
	CountOidcRoleMappings(ctx context.Context) (int64, error)
	GetOidcRoleMapping(ctx context.Context, id uuid.UUID) (dbq.OidcRoleMapping, error)
	GetOidcRoleMappingByIdpRole(ctx context.Context, idpRole string) (dbq.OidcRoleMapping, error)
	CreateOidcRoleMapping(ctx context.Context, arg dbq.CreateOidcRoleMappingParams) (dbq.OidcRoleMapping, error)
	UpdateOidcRoleMapping(ctx context.Context, arg dbq.UpdateOidcRoleMappingParams) (dbq.OidcRoleMapping, error)
	DeleteOidcRoleMapping(ctx context.Context, id uuid.UUID) (int64, error)

	// system_settings access for /admin/system/dns-settings.
	GetSystemSetting(ctx context.Context, key string) (dbq.SystemSetting, error)
	UpsertSystemSetting(ctx context.Context, arg dbq.UpsertSystemSettingParams) error
	DeleteSystemSetting(ctx context.Context, key string) error
}

type Handler struct {
	Q     Querier
	Audit audit.Recorder
	// DefaultDnsRecursiveUpstreams is the env-backed fallback used by
	// GET /admin/system/dns-settings when the system_settings row is
	// absent (or empty). Wired from main.go from
	// DCIM_DNS_RECURSIVE_UPSTREAMS (comma-separated); default is
	// {"1.1.1.1", "8.8.8.8"} to match Python's settings.py.
	DefaultDnsRecursiveUpstreams []string
}

func (h *Handler) Mount(r chi.Router) {
	r.Route("/admin", func(r chi.Router) {
		r.With(auth.RequireCapability("admin:users:read")).Get("/users", h.listUsers)
		r.With(auth.RequireCapability("admin:users:create")).Post("/users", h.createUser)
		r.With(auth.RequireCapability("admin:users:update")).Patch("/users/{id}", h.updateUser)
		// Local password set/reset for admin-created users. Same
		// mutation cap as updateUser — setting a password IS a user
		// mutation, and a separate cap would just fragment role bundles.
		r.With(auth.RequireCapability("admin:users:update")).Post("/users/{id}/password", h.setUserPassword)

		r.With(auth.RequireCapability("admin:roles:read")).Get("/roles", h.listRoles)
		r.With(auth.RequireCapability("admin:roles:create")).Post("/roles", h.createRole)
		r.With(auth.RequireCapability("admin:roles:update")).Patch("/roles/{id}", h.updateRole)
		r.With(auth.RequireCapability("admin:roles:delete")).Delete("/roles/{id}", h.deleteRole)

		r.With(auth.RequireCapability("admin:users:read")).Get("/users/{id}/assignments", h.listUserAssignments)
		r.With(auth.RequireCapability("admin:users:update")).Post("/assignments", h.createAssignment)
		r.With(auth.RequireCapability("admin:users:update")).Delete("/assignments/{id}", h.deleteAssignment)

		r.With(auth.RequireCapability("admin:oidc-mappings:read")).Get("/oidc-role-mappings", h.listOidcMappings)
		r.With(auth.RequireCapability("admin:oidc-mappings:create")).Post("/oidc-role-mappings", h.createOidcMapping)
		r.With(auth.RequireCapability("admin:oidc-mappings:update")).Patch("/oidc-role-mappings/{id}", h.updateOidcMapping)
		r.With(auth.RequireCapability("admin:oidc-mappings:delete")).Delete("/oidc-role-mappings/{id}", h.deleteOidcMapping)

		// Static capability catalog for the role-create picker UI; gated
		// on admin:roles:read because the picker is reachable only when
		// editing a role. Static for the lifetime of the process.
		r.With(auth.RequireCapability("admin:roles:read")).Get("/capabilities/catalog", h.getCapabilitiesCatalog)

		// System-wide DNS recursive_upstreams override — runtime-editable
		// alternative to the env-backed default. system-settings:read
		// gates the GET (and feeds the "current vs default" UI affordance);
		// system-settings:update gates the PUT (which audits + upserts).
		r.With(auth.RequireCapability("admin:system-settings:read")).Get("/system/dns-settings", h.getSystemDnsSettings)
		r.With(auth.RequireCapability("admin:system-settings:update")).Put("/system/dns-settings", h.putSystemDnsSettings)
	})
}

// ---- list ----

type listResponse struct {
	Items  []dbq.ListAdminUsersRow `json:"items"`
	Total  int64      `json:"total"`
	Limit  int32      `json:"limit"`
	Offset int32      `json:"offset"`
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	items, err := h.Q.ListAdminUsers(r.Context(), dbq.ListAdminUsersParams{
		Limit: limit, Offset: offset,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountAdminUsers(r.Context())
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse{
		Items: items, Total: total, Limit: limit, Offset: offset,
	})
}

// ---- create ----

type createReq struct {
	Email       string  `json:"email"`
	DisplayName *string `json:"display_name"`
	IsActive    *bool   `json:"is_active"`
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" {
		httpx.Error(w, http.StatusBadRequest, "email required")
		return
	}
	// Pre-check for duplicate so we 409 cleanly instead of leaning
	// on the unique-constraint error from PG (which would still
	// 5xx through the mapper).
	if _, err := h.Q.GetUserByEmail(r.Context(), req.Email); err == nil {
		httpx.Error(w, http.StatusConflict, "a user with that email already exists")
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	active := true
	if req.IsActive != nil {
		active = *req.IsActive
	}
	out, err := h.Q.CreateAdminUser(r.Context(), dbq.CreateAdminUserParams{
		Email: req.Email, DisplayName: req.DisplayName, IsActive: active,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "user.create", TargetType: "user", TargetID: out.ID.String(),
	})
	httpx.JSON(w, http.StatusCreated, out)
}

// ---- update ----

// updateReq tracks which fields the JSON payload set so we can
// distinguish "absent" from "explicit null" for display_name.
type updateReq struct {
	DisplayName    *string
	displayNameSet bool
	IsActive       *bool
}

func (u *updateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["display_name"]; ok {
		u.displayNameSet = true
		_ = json.Unmarshal(v, &u.DisplayName)
	}
	if v, ok := raw["is_active"]; ok {
		_ = json.Unmarshal(v, &u.IsActive)
	}
	return nil
}

func (h *Handler) updateUser(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	var req updateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdateAdminUser(r.Context(), dbq.UpdateAdminUserParams{
		ID: id, DisplayNameSet: req.displayNameSet,
		DisplayName: req.DisplayName, IsActive: req.IsActive,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "user not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "user.update", TargetType: "user", TargetID: id.String(),
	})
	httpx.JSON(w, http.StatusOK, out)
}
