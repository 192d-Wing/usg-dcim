package httpx

import (
	"net/url"
	"strconv"
)

// Page is the canonical {Items, Total, Limit, Offset} wrapper otter-go
// uses for paginated responses. Historically each handler package
// declared its own concrete page struct (logPage, alertsPage,
// rulesPage, assetsPage, …) — 10+ near-identical copies, and at least
// one of them (alerts) returned `Items: nil` from the empty-scope
// short-circuit, which encoding/json marshals as `null` and breaks
// finch's `data.items.map(...)` rendering.
//
// New handlers should:
//   - declare `type assetsPage = httpx.Page[Asset]` (alias) for the
//     wire-shape type they hand to JSON,
//   - use httpx.EmptyPage[Asset](limit, offset) wherever they need
//     to short-circuit so the empty-array invariant is structurally
//     enforced instead of relying on every author to remember
//     `Items: []Asset{}`.
//
// Old handlers can migrate opportunistically; the generic type is
// fully wire-compatible with the existing per-handler structs (same
// JSON field names + ordering).
type Page[T any] struct {
	Items  []T   `json:"items"`
	Total  int64 `json:"total"`
	Limit  int32 `json:"limit"`
	Offset int32 `json:"offset"`
}

// EmptyPage constructs the zero-result variant of Page[T] with an
// explicit non-nil empty Items slice. Use this instead of a struct
// literal in handler short-circuit paths so the json output is `[]`,
// not `null`.
func EmptyPage[T any](limit, offset int32) Page[T] {
	return Page[T]{
		Items:  []T{},
		Total:  0,
		Limit:  limit,
		Offset: offset,
	}
}

// PageBounds parses a paginated list query's limit + offset from URL
// values, resolving the three conventions otter-go has had to support:
//
//   - canonical: ?limit=N&offset=M (API tokens, curl, scripts)
//   - finch / Refine data-provider: ?page=N&page_size=M (1-indexed)
//   - mixed: ?page_size=N&offset=M (an older finch path)
//
// Precedence is "explicit wins": ?limit beats ?page_size, ?offset
// beats ?page→(page-1)*limit. Bounds match what every handler used to
// inline by hand (default page_size=50, clamped [1, 500]; default
// offset=0, clamped [0, 1_000_000]).
//
// Returns clamped int32 values ready to pass straight into a
// ListXParams.Limit / .Offset. Centralised here so a future
// pagination-shape change is one edit instead of fifty.
func PageBounds(q url.Values) (limit, offset int32) {
	limit = parseBoundedInt32(firstNonEmpty(q.Get("limit"), q.Get("page_size")), 50, 1, 500)
	if v := q.Get("offset"); v != "" {
		offset = parseBoundedInt32(v, 0, 0, 1_000_000)
		return
	}
	if v := q.Get("page"); v != "" {
		page := parseBoundedInt32(v, 1, 1, 1_000_000)
		offset = (page - 1) * limit
	}
	return
}

// parseBoundedInt32 returns the int32 form of s clamped to [lo, hi].
// Empty or unparseable s yields def. Used by PageBounds; not exported
// because every other handler-local int32 parser has slightly
// different defaults and is better off staying handler-local.
func parseBoundedInt32(s string, def, lo, hi int32) int32 {
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

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
