// Package bgp holds GET handlers for the BGP policy resources:
// asns, prefix-lists, prefix-list-entries. Deferred to follow-up:
// tcp-ao-key-chains, tcp-ao-keys, community-lists, community-list-entries,
// route-maps, route-map-entries.
package bgp

import (
	"context"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type Querier interface {
	ListAsns(ctx context.Context, arg dbq.ListAsnsParams) ([]dbq.Asn, error)
	CountAsns(ctx context.Context, arg dbq.CountAsnsParams) (int64, error)
	ListPrefixLists(ctx context.Context, arg dbq.ListPrefixListsParams) ([]dbq.PrefixList, error)
	CountPrefixLists(ctx context.Context, arg dbq.CountPrefixListsParams) (int64, error)
	ListPrefixListEntries(ctx context.Context, arg dbq.ListPrefixListEntriesParams) ([]dbq.PrefixListEntry, error)
	CountPrefixListEntries(ctx context.Context, arg dbq.CountPrefixListEntriesParams) (int64, error)
}

type Handler struct {
	Q Querier
}

func (h *Handler) Mount(r chi.Router) {
	r.Get("/asns", h.listAsns)
	r.Get("/prefix-lists", h.listPrefixLists)
	r.Get("/prefix-list-entries", h.listPrefixListEntries)
}

type asnsPage struct {
	Items  []dbq.Asn `json:"items"`
	Total  int64     `json:"total"`
	Limit  int32     `json:"limit"`
	Offset int32     `json:"offset"`
}

func (h *Handler) listAsns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(q.Get("limit"), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
	params := dbq.ListAsnsParams{Limit: limit, Offset: offset, Kind: strPtr(q.Get("kind"))}
	items, err := h.Q.ListAsns(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountAsns(r.Context(), dbq.CountAsnsParams{Kind: params.Kind})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, asnsPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

type prefixListsPage struct {
	Items  []dbq.PrefixList `json:"items"`
	Total  int64            `json:"total"`
	Limit  int32            `json:"limit"`
	Offset int32            `json:"offset"`
}

func (h *Handler) listPrefixLists(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(q.Get("limit"), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
	params := dbq.ListPrefixListsParams{Limit: limit, Offset: offset, Family: strPtr(q.Get("family"))}
	items, err := h.Q.ListPrefixLists(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountPrefixLists(r.Context(), dbq.CountPrefixListsParams{Family: params.Family})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, prefixListsPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

type prefixListEntriesPage struct {
	Items  []dbq.PrefixListEntry `json:"items"`
	Total  int64                 `json:"total"`
	Limit  int32                 `json:"limit"`
	Offset int32                 `json:"offset"`
}

func (h *Handler) listPrefixListEntries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(q.Get("limit"), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
	params := dbq.ListPrefixListEntriesParams{Limit: limit, Offset: offset}
	if v := q.Get("prefix_list_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "prefix_list_id is not a uuid")
			return
		}
		params.PrefixListID = &id
	}
	items, err := h.Q.ListPrefixListEntries(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountPrefixListEntries(r.Context(), dbq.CountPrefixListEntriesParams{PrefixListID: params.PrefixListID})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, prefixListEntriesPage{Items: items, Total: total, Limit: limit, Offset: offset})
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
