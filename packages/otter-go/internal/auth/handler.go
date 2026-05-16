package auth

import (
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"

	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// Handler mounts the read-side auth endpoints under /auth/. Write
// endpoints (oidc/login, oidc/callback, login, logout, refresh,
// tokens) ship in PR 36 / PR 37.
type Handler struct{ Q Querier }

func (h *Handler) Mount(r chi.Router) {
	r.Route("/auth", func(r chi.Router) {
		r.Get("/me", h.me)
	})
}

type meUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

type meResponse struct {
	User         *meUser  `json:"user"`
	ViaToken     bool     `json:"via_token"`
	Capabilities []string `json:"capabilities"`
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	p, ok := From(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "no principal")
		return
	}
	caps := append([]string(nil), p.Capabilities...)
	sort.Strings(caps)
	resp := meResponse{ViaToken: false, Capabilities: caps}
	// Best-effort user fetch — a missing user just means the JWT is
	// orphan (user deleted post-mint); still report caps + id so the
	// SPA can render something rather than 500.
	if u, err := h.Q.GetUser(r.Context(), p.Subject); err == nil {
		resp.User = &meUser{ID: u.ID.String(), Email: u.Email}
	} else {
		resp.User = &meUser{ID: p.Subject.String()}
	}
	httpx.JSON(w, http.StatusOK, resp)
}
