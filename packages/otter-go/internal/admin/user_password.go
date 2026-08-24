package admin

import (
	"encoding/json"
	"net/http"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// minPasswordRunes matches the break-glass local-login policy floor.
const minPasswordRunes = 12

type setPasswordReq struct {
	Password string `json:"password"`
}

// setUserPassword implements POST /admin/users/{id}/password — local
// password set/reset for admin-created users (UX-debt batch). Before
// this endpoint, a user created through the admin UI could never
// obtain a local password: createUser has no password field (by
// design — the admin shouldn't pick it silently) and no other writer
// of users.password_hash exists outside the OIDC upsert path.
func (h *Handler) setUserPassword(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	var req setPasswordReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	if utf8.RuneCountInString(req.Password) < minPasswordRunes {
		httpx.Error(w, http.StatusBadRequest, "password must be at least 12 characters")
		return
	}
	// bcrypt refuses inputs longer than 72 bytes; surface that as a
	// client error instead of letting GenerateFromPassword 500.
	if len(req.Password) > 72 {
		httpx.Error(w, http.StatusBadRequest, "password must be at most 72 bytes")
		return
	}
	// Same package + cost as the auth login path (bcrypt.DefaultCost),
	// so CompareHashAndPassword in internal/auth verifies these hashes.
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "password hashing failed")
		return
	}
	rows, err := h.Q.SetUserPasswordHash(r.Context(), dbq.SetUserPasswordHashParams{
		PasswordHash: string(hash), ID: id,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if rows == 0 {
		httpx.Error(w, http.StatusNotFound, "user not found")
		return
	}
	// Audit the act, never the material: no password, no hash, in the
	// metadata (or anywhere else in the row).
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "admin.user.password_set", TargetType: "user", TargetID: id.String(),
	})
	w.WriteHeader(http.StatusNoContent)
}
