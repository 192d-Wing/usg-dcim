package httpx

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
