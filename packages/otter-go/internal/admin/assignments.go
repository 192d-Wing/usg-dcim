// PR 76 — /admin/users/{id}/assignments + /admin/assignments CRUD.
//
// An assignment links a User to a Role + 0..N scope rows. Scopes
// are ABAC dimensions (region/site/fabric/etc.) — the scope_type
// enum is shared with oidc_role_mappings.scope_dimension.
//
// Multi-table writes (assignment + scope rows) run as sequential
// inserts rather than wrapped in a transaction. The existing Go
// codebase doesn't expose pgx transactions on the Querier
// interface, so partial-failure semantics match the rest of the
// surface: on a scope INSERT error, the assignment row + earlier
// scopes are committed. Operator deletes + retries to clean up.
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
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// validScopeTypes mirrors the scope_type pg enum. Drops here also
// drop in role_scopes / oidc_role_mappings.
var validScopeTypes = map[string]struct{}{
	"global": {}, "region": {}, "site": {}, "site_group": {},
	"enclave": {}, "organization": {}, "fabric": {}, "classification": {},
}

// ---- response shapes ----

type scopeRowOut struct {
	ID        uuid.UUID `json:"id"`
	ScopeType string    `json:"scope_type"`
	TargetID  *string   `json:"target_id"`
}

type assignmentOut struct {
	ID       uuid.UUID     `json:"id"`
	UserID   uuid.UUID     `json:"user_id"`
	RoleID   uuid.UUID     `json:"role_id"`
	RoleName string        `json:"role_name"`
	Scopes   []scopeRowOut `json:"scopes"`
}

// ---- list per user ----

func (h *Handler) listUserAssignments(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	// 404 if the user doesn't exist — matches Python (operator
	// listing a nonexistent user gets a clear error rather than
	// an empty array).
	if _, err := h.Q.GetUser(r.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "user not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	rows, err := h.Q.ListUserAssignments(r.Context(), id)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	out, err := h.hydrateAssignments(r.Context(), rows)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// hydrateAssignments turns N user_role rows into N assignmentOut
// in O(2) round-trips (one for scopes, one for role names) rather
// than N+1.
func (h *Handler) hydrateAssignments(
	ctx context.Context, rows []dbq.UserRoleRow,
) ([]assignmentOut, error) {
	if len(rows) == 0 {
		return []assignmentOut{}, nil
	}
	assignmentIDs := make([]uuid.UUID, len(rows))
	roleIDs := make([]uuid.UUID, 0, len(rows))
	for i, r := range rows {
		assignmentIDs[i] = r.ID
		roleIDs = append(roleIDs, r.RoleID)
	}
	scopeRows, err := h.Q.ListRoleScopesByAssignments(ctx, assignmentIDs)
	if err != nil {
		return nil, err
	}
	scopesByAssignment := make(map[uuid.UUID][]scopeRowOut, len(rows))
	for _, s := range scopeRows {
		scopesByAssignment[s.AssignmentID] = append(scopesByAssignment[s.AssignmentID],
			scopeRowOut{ID: s.ID, ScopeType: s.ScopeType, TargetID: s.TargetID})
	}
	names, err := h.Q.GetRoleNamesByIDs(ctx, roleIDs)
	if err != nil {
		return nil, err
	}
	nameByID := make(map[uuid.UUID]string, len(names))
	for _, n := range names {
		nameByID[n.ID] = n.Name
	}
	out := make([]assignmentOut, len(rows))
	for i, r := range rows {
		name := nameByID[r.RoleID]
		if name == "" {
			name = "(unknown)"
		}
		out[i] = assignmentOut{
			ID: r.ID, UserID: r.UserID, RoleID: r.RoleID,
			RoleName: name,
			Scopes:   scopesByAssignment[r.ID],
		}
		if out[i].Scopes == nil {
			out[i].Scopes = []scopeRowOut{}
		}
	}
	return out, nil
}

// ---- create ----

type scopeRowIn struct {
	ScopeType string  `json:"scope_type"`
	TargetID  *string `json:"target_id"`
}

type assignmentCreateReq struct {
	UserID uuid.UUID    `json:"user_id"`
	RoleID uuid.UUID    `json:"role_id"`
	Scopes []scopeRowIn `json:"scopes"`
}

func (h *Handler) createAssignment(w http.ResponseWriter, r *http.Request) {
	var req assignmentCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.UserID == uuid.Nil || req.RoleID == uuid.Nil {
		httpx.Error(w, http.StatusBadRequest, "user_id and role_id required")
		return
	}
	// Validate scope_type up front before any writes.
	for _, s := range req.Scopes {
		if _, ok := validScopeTypes[s.ScopeType]; !ok {
			httpx.Error(w, http.StatusBadRequest,
				"unknown scope_type "+s.ScopeType)
			return
		}
	}
	// Verify both sides exist (separate 404s so the operator sees
	// which one was wrong).
	if _, err := h.Q.GetUser(r.Context(), req.UserID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "user not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if _, err := h.Q.GetAdminRole(r.Context(), req.RoleID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "role not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	// Dup-check: a (user, role) pair can only be assigned once.
	// The UNIQUE constraint would catch a race but the pre-check
	// gives a clean 409.
	if _, err := h.Q.FindUserRoleByUserAndRole(r.Context(), req.UserID, req.RoleID); err == nil {
		httpx.Error(w, http.StatusConflict, "user is already assigned to this role")
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	assignment, err := h.Q.CreateUserRole(r.Context(), req.UserID, req.RoleID)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	// Sequential scope inserts — see file header for partial-
	// failure semantics. The Python doesn't expose a true
	// transaction either; SQLAlchemy's implicit session.commit
	// at the end is the closest equivalent.
	for _, s := range req.Scopes {
		if _, err := h.Q.CreateRoleScope(r.Context(), dbq.CreateRoleScopeParams{
			AssignmentID: assignment.ID, ScopeType: s.ScopeType, TargetID: s.TargetID,
		}); err != nil {
			status, msg := httpx.Mapped(err)
			httpx.Error(w, status, msg)
			return
		}
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action:     "role_assignment.create",
		TargetType: "user_role",
		TargetID:   assignment.ID.String(),
	})
	out, err := h.hydrateAssignments(r.Context(), []dbq.UserRoleRow{assignment})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusCreated, out[0])
}

// ---- delete ----

func (h *Handler) deleteAssignment(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	// Pre-lookup so we 404 distinctly from "delete touched 0 rows."
	if _, err := h.Q.GetUserRole(r.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "assignment not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	// Scope rows first — the FK has no ON DELETE behavior so the
	// parent delete would error if children still existed.
	if err := h.Q.DeleteRoleScopesForAssignment(r.Context(), id); err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if _, err := h.Q.DeleteUserRole(r.Context(), id); err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "role_assignment.delete", TargetType: "user_role", TargetID: id.String(),
	})
	w.WriteHeader(http.StatusNoContent)
}
