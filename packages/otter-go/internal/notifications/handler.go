// Package notifications holds GET handlers for /api/v1/notifications.
// Currently: channels list. Deferred: channel create/patch/test
// (writes — Phase 2), and per-alert delivery history if that ever
// becomes a read endpoint.
package notifications

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type Querier interface {
	ListNotificationChannels(ctx context.Context, arg dbq.ListNotificationChannelsParams) ([]dbq.NotificationChannel, error)
	CountNotificationChannels(ctx context.Context) (int64, error)
	GetNotificationChannel(ctx context.Context, id uuid.UUID) (dbq.NotificationChannel, error)

	// Mutations (PR 45)
	CreateNotificationChannel(ctx context.Context, arg dbq.CreateNotificationChannelParams) (dbq.NotificationChannel, error)
	UpdateNotificationChannel(ctx context.Context, arg dbq.UpdateNotificationChannelParams) (dbq.NotificationChannel, error)
	DeleteNotificationChannel(ctx context.Context, id uuid.UUID) error
}

type Handler struct {
	Q     Querier
	Audit audit.Recorder
}

func (h *Handler) Mount(r chi.Router) {
	// Cap codes match Python's notifications.py + the canonical catalog
	// in internal/admin/capabilities.go. Earlier values lived under
	// `alerts:notifications:*` which doesn't exist in either catalog —
	// role assignments using the correct `notifications:channels:*`
	// codes silently failed to grant access, so the routes were
	// reachable only by global (`*`) principals. LIST also gains a
	// read gate so a cap-less authenticated user can't enumerate
	// every channel in the fleet (same shape sites/racks/assets fixed
	// in PR #195).
	r.Route("/notifications", func(r chi.Router) {
		r.With(auth.RequireCapability(capChannelsRead)).Get("/channels", h.listChannels)
		r.With(auth.RequireCapability(capChannelsCreate)).Post("/channels", h.createChannel)
		r.With(auth.RequireCapability(capChannelsUpdate)).Patch("/channels/{id}", h.updateChannel)
		r.With(auth.RequireCapability(capChannelsDelete)).Delete("/channels/{id}", h.deleteChannel)
		// Test endpoint reuses the channels:update capability — Python's
		// (api/notifications.py:106) anchors here too. Cap-wise it's
		// "did this principal configure the channel?" not a separate
		// test-only privilege.
		r.With(auth.RequireCapability(capChannelsUpdate)).Post("/channels/{id}/test", h.testChannel)
	})
}

const (
	capChannelsRead   = "notifications:channels:read"
	capChannelsCreate = "notifications:channels:create"
	capChannelsUpdate = "notifications:channels:update"
	capChannelsDelete = "notifications:channels:delete"
)

type channelsPage struct {
	Items  []dbq.NotificationChannel `json:"items"`
	Total  int64                     `json:"total"`
	Limit  int32                     `json:"limit"`
	Offset int32                     `json:"offset"`
}

func (h *Handler) listChannels(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
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
