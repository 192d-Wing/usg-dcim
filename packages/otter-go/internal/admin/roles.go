// PR 75 — /admin/roles CRUD.
//
// Roles carry a list of permission codes that get materialized
// into a Principal's capability set at login. The API enforces
// **no-escalation** on create + update: the caller cannot grant
// codes they don't themselves hold (wildcard-aware via
// auth.HasCapability). System roles are read-only and undeletable;
// the migration bootstrap creates them and the API protects them
// from being mutated even by users with admin:roles:* caps.
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
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// ---- list ----

type listRolesResponse struct {
	Items  []dbq.Role `json:"items"`
	Total  int64      `json:"total"`
	Limit  int32      `json:"limit"`
	Offset int32      `json:"offset"`
}

func (h *Handler) listRoles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(q.Get("limit"), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
	items, err := h.Q.ListAdminRoles(r.Context(), dbq.ListAdminRolesParams{
		Limit: limit, Offset: offset,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountAdminRoles(r.Context())
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, listRolesResponse{
		Items: items, Total: total, Limit: limit, Offset: offset,
	})
}

// ---- create ----

type roleCreateReq struct {
	Name            string   `json:"name"`
	Description     *string  `json:"description"`
	PermissionCodes []string `json:"permission_codes"`
}

// missingCaps returns the codes in `want` that the principal can't
// already grant (no-escalation check). Wildcard semantics matched
// to auth.HasCapability — so "admin:*" grants every admin:foo code.
func missingCaps(held, want []string) []string {
	var missing []string
	for _, c := range want {
		if !auth.HasCapability(held, c) {
			missing = append(missing, c)
		}
	}
	return missing
}

func (h *Handler) createRole(w http.ResponseWriter, r *http.Request) {
	var req roleCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		httpx.Error(w, http.StatusBadRequest, "name required")
		return
	}
	if _, err := h.Q.GetAdminRoleByName(r.Context(), req.Name); err == nil {
		httpx.Error(w, http.StatusConflict, "a role with that name already exists")
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	// No-escalation: refuse if any requested code isn't held by the
	// caller. Sorts the missing list for stable error output.
	p, _ := auth.From(r.Context())
	if extra := missingCaps(p.Capabilities, req.PermissionCodes); len(extra) > 0 {
		// Stable order so the error message diffs cleanly.
		extraJSON, _ := json.Marshal(extra)
		httpx.Error(w, http.StatusBadRequest,
			"cannot grant capabilities you don't hold: "+string(extraJSON))
		return
	}
	codes, err := json.Marshal(req.PermissionCodes)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "permission_codes must be a JSON array")
		return
	}
	out, err := h.Q.CreateAdminRole(r.Context(), dbq.CreateAdminRoleParams{
		Name: req.Name, Description: req.Description, PermissionCodes: codes,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "role.create", TargetType: "role", TargetID: out.ID.String(),
	})
	httpx.JSON(w, http.StatusCreated, out)
}

// ---- update ----

// roleUpdateReq tracks which fields were set so optional fields can
// distinguish "absent" from "explicit null/empty array".
type roleUpdateReq struct {
	Description        *string
	descriptionSet     bool
	PermissionCodes    []string
	permissionCodesSet bool
}

func (u *roleUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["description"]; ok {
		u.descriptionSet = true
		_ = json.Unmarshal(v, &u.Description)
	}
	if v, ok := raw["permission_codes"]; ok {
		u.permissionCodesSet = true
		_ = json.Unmarshal(v, &u.PermissionCodes)
	}
	return nil
}

func (h *Handler) updateRole(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	// Look up first to enforce is_system check + emit a 404 distinct
	// from "matched but is_system=true."
	existing, err := h.Q.GetAdminRole(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "role not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if existing.IsSystem {
		httpx.Error(w, http.StatusBadRequest, "system roles are read-only")
		return
	}
	var req roleUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	var codes json.RawMessage
	if req.permissionCodesSet {
		// No-escalation also applies on update: the caller must
		// hold every code they're granting (even ones the role
		// already had — keeps the surface symmetric with create).
		p, _ := auth.From(r.Context())
		if extra := missingCaps(p.Capabilities, req.PermissionCodes); len(extra) > 0 {
			extraJSON, _ := json.Marshal(extra)
			httpx.Error(w, http.StatusBadRequest,
				"cannot grant capabilities you don't hold: "+string(extraJSON))
			return
		}
		codes, _ = json.Marshal(req.PermissionCodes)
	}
	out, err := h.Q.UpdateAdminRole(r.Context(), dbq.UpdateAdminRoleParams{
		ID:                 id,
		DescriptionSet:     req.descriptionSet,
		Description:        req.Description,
		PermissionCodesSet: req.permissionCodesSet,
		PermissionCodes:    codes,
	})
	if err != nil {
		// The SQL's "AND is_system = FALSE" guard means a deleted-
		// concurrent-row OR is_system-flipped-true row returns 0
		// updates → pgx.ErrNoRows. We surfaced is_system above
		// already, so this is truly "row disappeared mid-update."
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "role not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "role.update", TargetType: "role", TargetID: id.String(),
	})
	httpx.JSON(w, http.StatusOK, out)
}

// ---- delete ----

func (h *Handler) deleteRole(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	existing, err := h.Q.GetAdminRole(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "role not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if existing.IsSystem {
		httpx.Error(w, http.StatusBadRequest, "system roles cannot be deleted")
		return
	}
	// Pre-check for assignments — refuse with 409 rather than
	// trigger an FK violation. There's still a tiny race window
	// where an assignment could be added between the count and
	// the delete; the FK constraint catches it as a 5xx via the
	// generic error mapper.
	count, err := h.Q.CountUserRolesForRole(r.Context(), id)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if count > 0 {
		httpx.Error(w, http.StatusConflict,
			"role is assigned to one or more users; remove assignments first")
		return
	}
	n, err := h.Q.DeleteAdminRole(r.Context(), id)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if n == 0 {
		// Lost the race — row disappeared (or flipped is_system).
		httpx.Error(w, http.StatusNotFound, "role not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "role.delete", TargetType: "role", TargetID: id.String(),
	})
	w.WriteHeader(http.StatusNoContent)
}
