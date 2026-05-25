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
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type Querier interface {
	ListAdminUsers(ctx context.Context, arg dbq.ListAdminUsersParams) ([]dbq.User, error)
	CountAdminUsers(ctx context.Context) (int64, error)
	GetUser(ctx context.Context, id uuid.UUID) (dbq.User, error)
	GetUserByEmail(ctx context.Context, email string) (dbq.User, error)
	CreateAdminUser(ctx context.Context, arg dbq.CreateAdminUserParams) (dbq.User, error)
	UpdateAdminUser(ctx context.Context, arg dbq.UpdateAdminUserParams) (dbq.User, error)

	ListAdminRoles(ctx context.Context, arg dbq.ListAdminRolesParams) ([]dbq.Role, error)
	CountAdminRoles(ctx context.Context) (int64, error)
	GetAdminRole(ctx context.Context, id uuid.UUID) (dbq.Role, error)
	GetAdminRoleByName(ctx context.Context, name string) (dbq.Role, error)
	CreateAdminRole(ctx context.Context, arg dbq.CreateAdminRoleParams) (dbq.Role, error)
	UpdateAdminRole(ctx context.Context, arg dbq.UpdateAdminRoleParams) (dbq.Role, error)
	DeleteAdminRole(ctx context.Context, id uuid.UUID) (int64, error)
	CountUserRolesForRole(ctx context.Context, roleID uuid.UUID) (int64, error)

	ListUserAssignments(ctx context.Context, userID uuid.UUID) ([]dbq.UserRoleRow, error)
	GetUserRole(ctx context.Context, id uuid.UUID) (dbq.UserRoleRow, error)
	FindUserRoleByUserAndRole(ctx context.Context, userID, roleID uuid.UUID) (dbq.UserRoleRow, error)
	CreateUserRole(ctx context.Context, userID, roleID uuid.UUID) (dbq.UserRoleRow, error)
	DeleteUserRole(ctx context.Context, id uuid.UUID) (int64, error)
	ListRoleScopesByAssignment(ctx context.Context, assignmentID uuid.UUID) ([]dbq.RoleScopeRow, error)
	ListRoleScopesByAssignments(ctx context.Context, ids []uuid.UUID) ([]dbq.RoleScopeRow, error)
	CreateRoleScope(ctx context.Context, arg dbq.CreateRoleScopeParams) (dbq.RoleScopeRow, error)
	DeleteRoleScopesForAssignment(ctx context.Context, assignmentID uuid.UUID) error
	GetRoleNamesByIDs(ctx context.Context, ids []uuid.UUID) ([]dbq.RoleNameRow, error)
}

type Handler struct {
	Q     Querier
	Audit audit.Recorder
}

func (h *Handler) Mount(r chi.Router) {
	r.Route("/admin", func(r chi.Router) {
		r.With(auth.RequireCapability("admin:users:read")).Get("/users", h.listUsers)
		r.With(auth.RequireCapability("admin:users:create")).Post("/users", h.createUser)
		r.With(auth.RequireCapability("admin:users:update")).Patch("/users/{id}", h.updateUser)

		r.With(auth.RequireCapability("admin:roles:read")).Get("/roles", h.listRoles)
		r.With(auth.RequireCapability("admin:roles:create")).Post("/roles", h.createRole)
		r.With(auth.RequireCapability("admin:roles:update")).Patch("/roles/{id}", h.updateRole)
		r.With(auth.RequireCapability("admin:roles:delete")).Delete("/roles/{id}", h.deleteRole)

		r.With(auth.RequireCapability("admin:users:read")).Get("/users/{id}/assignments", h.listUserAssignments)
		r.With(auth.RequireCapability("admin:users:update")).Post("/assignments", h.createAssignment)
		r.With(auth.RequireCapability("admin:users:update")).Delete("/assignments/{id}", h.deleteAssignment)
	})
}

// ---- list ----

type listResponse struct {
	Items  []dbq.User `json:"items"`
	Total  int64      `json:"total"`
	Limit  int32      `json:"limit"`
	Offset int32      `json:"offset"`
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(q.Get("limit"), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
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

func parseInt32(s string, def, lo, hi int32) int32 {
	if s == "" {
		return def
	}
	n, err := strconv.ParseInt(s, 10, 32)
	if err != nil {
		return def
	}
	v := int32(n)
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
