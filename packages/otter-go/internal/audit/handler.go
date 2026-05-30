// Package audit holds GET handlers for /api/v1/audit. List + distinct
// actions (drives UI dropdown). The Python audit module has more
// internal helpers but only these two are public read endpoints.
package audit

import (
	"context"
	"net/http"
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

// logPage is the wire shape for /audit/log. Alias of the canonical
// httpx.Page[T] generic so the {Items, Total, Limit, Offset} layout
// stays in lockstep with every other paginated handler and the
// empty-page constructor (httpx.EmptyPage) is reachable without
// importing into the handler body.
type logPage = httpx.Page[dbq.AuditLog]

// resolveScope is the shared preamble for both list handlers. It does
// three jobs in one place so the listLog / listActions bodies stay
// short + structurally identical, and so a missing piece of the chain
// (no principal, scope-expansion error, empty-scope short-circuit) is
// handled exactly once:
//
//  1. Pulls the Principal off the request context. If the context
//     somehow lacks one (would only happen if the cap-gate middleware
//     is bypassed — see the comment on Mount), we 500 rather than
//     fall through with a zero-value Principal, which would silently
//     resolve to GlobalScope and dump the entire audit log.
//  2. Calls auth.ScopedSiteFilter to expand the principal's
//     audit:events:read scope into the concrete site_id slice.
//  3. Short-circuits to an empty body when scope expands to zero
//     sites (matches Python's `if not in_scope: return empty_page(...)`).
//
// Returns (siteIDs, true) when the handler is done (response already
// written). Otherwise (siteIDs may be nil for global) returns
// (siteIDs, false) and the caller proceeds with the underlying query.
func (h *Handler) resolveScope(w http.ResponseWriter, r *http.Request, empty func()) (siteIDs []uuid.UUID, done bool) {
	p, ok := auth.From(r.Context())
	if !ok {
		// Defense-in-depth: cap-gate middleware should have already
		// stopped this request. If it didn't (wiring bug, future
		// refactor), don't silently elevate to global.
		httpx.Error(w, http.StatusInternalServerError, "missing principal")
		return nil, true
	}
	ids, scoped, err := auth.ScopedSiteFilter(r.Context(), h.Q, p, capRead)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return nil, true
	}
	if scoped && len(ids) == 0 {
		empty()
		return nil, true
	}
	return ids, false
}

func (h *Handler) listLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	scopeSiteIDs, done := h.resolveScope(w, r, func() {
		httpx.JSON(w, http.StatusOK, httpx.EmptyPage[dbq.AuditLog](limit, offset))
	})
	if done {
		return
	}
	params, ok := buildListParams(w, q, limit, offset, scopeSiteIDs)
	if !ok {
		return
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

// buildListParams parses the audit-log query string into a
// ListAuditLogParams. Returns ok=false (and writes a 400) on bad input.
// Centralised here so listLog stays a flat sequence of steps and the
// param-parsing branches don't push cognitive complexity over the lint
// ceiling. Enforces an additional rule the SQL filter can't: a scoped
// caller who passes ?site_id=X with X outside their reachable set
// receives 403, not silent 200/empty (which used to mask an authz
// failure as a no-data result).
func buildListParams(w http.ResponseWriter, q map[string][]string, limit, offset int32, scopeSiteIDs []uuid.UUID) (dbq.ListAuditLogParams, bool) {
	params := dbq.ListAuditLogParams{
		Limit: limit, Offset: offset,
		Action:       strPtr(qGet(q, "action")),
		TargetType:   strPtr(qGet(q, "target_type")),
		TargetID:     strPtr(qGet(q, "target_id")),
		ScopeSiteIds: scopeSiteIDs,
	}
	if !parseUUIDFilters(w, q, scopeSiteIDs, &params) {
		return params, false
	}
	if !parseTargetIDs(q, &params) {
		return params, false
	}
	if !parseTimeRange(w, q, &params) {
		return params, false
	}
	if v := qGet(q, "success"); v != "" {
		b := v == "true" || v == "1"
		params.Success = &b
	}
	return params, true
}

// parseUUIDFilters handles actor_user_id + site_id (the two UUID query
// params) and enforces the cross-cutting rule that a scoped caller may
// not pass a site_id outside their reachable set — that turns a silent
// 200/empty (SQL ANDs `site_id = X` AND `site_id = ANY(scope)`) into
// an explicit 403.
func parseUUIDFilters(w http.ResponseWriter, q map[string][]string, scopeSiteIDs []uuid.UUID, params *dbq.ListAuditLogParams) bool {
	if v := qGet(q, "actor_user_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "actor_user_id is not a uuid")
			return false
		}
		params.ActorUserID = &id
	}
	if v := qGet(q, "site_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "site_id is not a uuid")
			return false
		}
		// Reject out-of-scope site_id explicitly rather than letting
		// the SQL AND-of-(=site AND IN scope) silently return empty.
		// Mirrors EnforceSiteScope on mutation handlers.
		if !siteInScope(id, scopeSiteIDs) {
			httpx.Error(w, http.StatusForbidden, "site_id is outside your scope")
			return false
		}
		params.SiteID = &id
	}
	return true
}

// parseTargetIDs handles the explicit-empty signal from splitCSV so a
// caller passing `?target_ids=,` doesn't get a sentinel string in the
// SQL ANY() filter (the old `__dcim_no_target_ids__` value could collide
// with a legitimate target_id). pgx encodes []string{} as the SQL array
// literal '{}'; `target_id = ANY('{}')` is FALSE for every row, which
// is the intended semantic.
func parseTargetIDs(q map[string][]string, params *dbq.ListAuditLogParams) bool {
	ids, explicitlyEmpty := splitCSV(qGet(q, "target_ids"))
	switch {
	case explicitlyEmpty:
		params.TargetIDs = []string{}
	case len(ids) > 0:
		params.TargetIDs = ids
	}
	return true
}

// parseTimeRange reads ?since= and ?until= as RFC3339. Either or both
// may be absent.
func parseTimeRange(w http.ResponseWriter, q map[string][]string, params *dbq.ListAuditLogParams) bool {
	for _, f := range []struct {
		key string
		dst **time.Time
	}{
		{"since", &params.Since},
		{"until", &params.Until},
	} {
		v := qGet(q, f.key)
		if v == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, f.key+" is not RFC3339")
			return false
		}
		*f.dst = &t
	}
	return true
}

// siteInScope returns true when the principal's expansion contains
// siteID, OR when scopeSiteIDs is nil (caller is global — every site
// is in scope). Mirrors EnforceSiteScope on read paths.
func siteInScope(siteID uuid.UUID, scopeSiteIDs []uuid.UUID) bool {
	if scopeSiteIDs == nil {
		return true
	}
	for _, id := range scopeSiteIDs {
		if id == siteID {
			return true
		}
	}
	return false
}

func (h *Handler) listActions(w http.ResponseWriter, r *http.Request) {
	// The action vocabulary varies per principal (a scoped admin
	// sees fewer action codes than a global admin — NULL-site
	// events like `login.failed` are filtered out by ABAC). Any
	// shared HTTP cache between users would conflate vocabularies
	// and cause finch's filter dropdown to show codes that produce
	// empty result sets on /audit/log. Tell intermediaries the
	// response is per-user so a shared edge cache or a SPA SW that
	// keys on URL alone doesn't reuse it across sessions.
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("Vary", "Authorization")
	scopeSiteIDs, done := h.resolveScope(w, r, func() {
		httpx.JSON(w, http.StatusOK, []string{})
	})
	if done {
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

// splitCSV parses a comma-separated query value into a deduped, trimmed
// list. Returns (nil, false) when the parameter was absent or blank
// (caller should not filter); (nil, true) when the caller passed a
// non-empty value that contained only commas/whitespace (caller asked
// for "no matches" explicitly — handler short-circuits); and (slice,
// false) for normal input.
func splitCSV(s string) (ids []string, explicitlyEmpty bool) {
	if s == "" {
		return nil, false
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
		// The caller passed something like "," or " , " — they
		// intentionally narrowed to zero target_ids. Signal that
		// without injecting a sentinel string into the SQL ANY()
		// filter (the old `__dcim_no_target_ids__` value could
		// collide with a real target_id).
		return nil, true
	}
	return out, false
}

func first(q map[string][]string, key string) string {
	if vs := q[key]; len(vs) > 0 {
		return vs[0]
	}
	return ""
}

// qGet is the map-keyed sibling of url.Values.Get — buildListParams
// works against the raw map so the helper can be unit-tested without
// constructing a *http.Request. Identical to `first`; kept named at
// the call sites for readability.
var qGet = first
