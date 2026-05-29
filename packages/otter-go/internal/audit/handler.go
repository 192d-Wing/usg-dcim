// Package audit holds GET handlers for /api/v1/audit. List + distinct
// actions (drives UI dropdown). The Python audit module has more
// internal helpers but only these two are public read endpoints.
package audit

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type Querier interface {
	ListAuditLog(ctx context.Context, arg dbq.ListAuditLogParams) ([]dbq.AuditLog, error)
	CountAuditLog(ctx context.Context, arg dbq.CountAuditLogParams) (int64, error)
	ListAuditActions(ctx context.Context, arg dbq.ListAuditActionsParams) ([]string, error)
	// Embedded so we can call auth.ScopedSiteFilter — site-scope
	// expansion walks direct + region + site-group + organization.
	ListSiteIDsForExpansion(ctx context.Context, arg dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error)
}

type Handler struct {
	Q Querier
}

// capRead is the capability both /log and /actions gate on, and the
// scope code ScopedSiteFilter uses to compute the per-caller site set.
const capRead = "audit:events:read"

func (h *Handler) Mount(r chi.Router) {
	r.Route("/audit", func(r chi.Router) {
		// Mirror api/audit.py — both endpoints require capRead.
		// Without these wraps any authenticated principal could read
		// the full audit log.
		r.With(auth.RequireCapability(capRead)).Get("/log", h.listLog)
		r.With(auth.RequireCapability(capRead)).Get("/actions", h.listActions)
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
	limit := parseInt32(pageSize(q), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
	p, _ := auth.From(r.Context())
	scopeSiteIDs, scoped, err := auth.ScopedSiteFilter(r.Context(), h.Q, p, capRead)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	// Scoped caller whose scope expands to zero sites: short-circuit.
	// Mirrors Python's `if not in_scope: return empty_page(...)`.
	if scoped && len(scopeSiteIDs) == 0 {
		httpx.JSON(w, http.StatusOK, logPage{Items: []dbq.AuditLog{}, Total: 0, Limit: limit, Offset: offset})
		return
	}
	params := dbq.ListAuditLogParams{
		Limit: limit, Offset: offset,
		Action:       strPtr(q.Get("action")),
		TargetType:   strPtr(q.Get("target_type")),
		TargetID:     strPtr(q.Get("target_id")),
		ScopeSiteIds: scopeSiteIDs,
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
	if ids := splitCSV(q.Get("target_ids")); len(ids) > 0 {
		params.TargetIDs = ids
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
		ActorUserID:  params.ActorUserID, Action: params.Action,
		TargetType:   params.TargetType, TargetID: params.TargetID,
		TargetIDs:    params.TargetIDs,
		SiteID:       params.SiteID, Since: params.Since, Until: params.Until,
		Success:      params.Success,
		ScopeSiteIds: params.ScopeSiteIds,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, logPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

func (h *Handler) listActions(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	scopeSiteIDs, scoped, err := auth.ScopedSiteFilter(r.Context(), h.Q, p, capRead)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	// Scoped caller whose scope expands to zero sites should see an
	// empty action list — Python returns `[]` in this case (audit.py).
	if scoped && len(scopeSiteIDs) == 0 {
		httpx.JSON(w, http.StatusOK, []string{})
		return
	}
	actions, err := h.Q.ListAuditActions(r.Context(), dbq.ListAuditActionsParams{
		ScopeSiteIds: scopeSiteIDs,
	})
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

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	seen := make(map[string]struct{})
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		out = append(out, part)
	}
	if len(out) == 0 {
		return []string{"__dcim_no_target_ids__"}
	}
	return out
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
