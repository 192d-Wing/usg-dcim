// Package notifications holds GET handlers for /api/v1/notifications.
// Currently: channels list. Deferred: channel create/patch/test
// (writes — Phase 2), and per-alert delivery history if that ever
// becomes a read endpoint.
package notifications

import (
	"context"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type Querier interface {
	ListNotificationChannels(ctx context.Context, arg dbq.ListNotificationChannelsParams) ([]dbq.NotificationChannel, error)
	CountNotificationChannels(ctx context.Context) (int64, error)
}

type Handler struct {
	Q Querier
}

func (h *Handler) Mount(r chi.Router) {
	r.Route("/notifications", func(r chi.Router) {
		r.Get("/channels", h.listChannels)
	})
}

type channelsPage struct {
	Items  []dbq.NotificationChannel `json:"items"`
	Total  int64                     `json:"total"`
	Limit  int32                     `json:"limit"`
	Offset int32                     `json:"offset"`
}

func (h *Handler) listChannels(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(pageSize(q), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
	items, err := h.Q.ListNotificationChannels(r.Context(), dbq.ListNotificationChannelsParams{
		Limit: limit, Offset: offset,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountNotificationChannels(r.Context())
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, channelsPage{Items: items, Total: total, Limit: limit, Offset: offset})
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

func pageSize(q map[string][]string) string {
	if v := first(q, "limit"); v != "" {
		return v
	}
	return first(q, "page_size")
}

func first(q map[string][]string, key string) string {
	if vs := q[key]; len(vs) > 0 {
		return vs[0]
	}
	return ""
}
