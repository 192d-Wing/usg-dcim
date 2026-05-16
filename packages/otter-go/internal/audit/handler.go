// Package audit holds GET handlers for /api/v1/audit. List + distinct
// actions (drives UI dropdown). The Python audit module has more
// internal helpers but only these two are public read endpoints.
package audit

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type Querier interface {
	ListAuditLog(ctx context.Context, arg dbq.ListAuditLogParams) ([]dbq.AuditLog, error)
	CountAuditLog(ctx context.Context, arg dbq.CountAuditLogParams) (int64, error)
	ListAuditActions(ctx context.Context) ([]string, error)
}

type Handler struct {
	Q Querier
}

func (h *Handler) Mount(r chi.Router) {
	r.Route("/audit", func(r chi.Router) {
		r.Get("/log", h.listLog)
		r.Get("/actions", h.listActions)
	})
}

type logPage struct {
	Items  []dbq.AuditLog `json:"items"`
	Total  int64          `json:"total"`
	Limit  int32          `json:"limit"`
	Offset int32          `json:"offset"`
}

func (h *Handler) listLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(q.Get("limit"), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
	params := dbq.ListAuditLogParams{
		Limit: limit, Offset: offset,
		Action:     strPtr(q.Get("action")),
		TargetType: strPtr(q.Get("target_type")),
		TargetID:   strPtr(q.Get("target_id")),
	}
	if v := q.Get("actor_user_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "actor_user_id is not a uuid")
			return
		}
		params.ActorUserID = &id
	}
	if v := q.Get("site_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "site_id is not a uuid")
			return
		}
		params.SiteID = &id
	}
	for _, f := range []struct {
		key string
		dst **time.Time
	}{
		{"since", &params.Since},
		{"until", &params.Until},
	} {
		if v := q.Get(f.key); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				httpx.Error(w, http.StatusBadRequest, f.key+" is not RFC3339")
				return
			}
			*f.dst = &t
		}
	}
	if v := q.Get("success"); v != "" {
		b := v == "true" || v == "1"
		params.Success = &b
	}
	items, err := h.Q.ListAuditLog(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountAuditLog(r.Context(), dbq.CountAuditLogParams{
		ActorUserID: params.ActorUserID, Action: params.Action,
		TargetType: params.TargetType, TargetID: params.TargetID,
		SiteID: params.SiteID, Since: params.Since, Until: params.Until,
		Success: params.Success,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, logPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

func (h *Handler) listActions(w http.ResponseWriter, r *http.Request) {
	actions, err := h.Q.ListAuditActions(r.Context())
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if actions == nil {
		actions = []string{}
	}
	httpx.JSON(w, http.StatusOK, actions)
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func parseInt32(s string, def, lo, hi int32) int32 {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
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
