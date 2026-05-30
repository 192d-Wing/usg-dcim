// Package bgp holds GET handlers for the BGP policy resources:
// asns, prefix-lists, prefix-list-entries. Deferred to follow-up:
// tcp-ao-key-chains, tcp-ao-keys, community-lists, community-list-entries,
// route-maps, route-map-entries.
package bgp

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
	ListAsns(ctx context.Context, arg dbq.ListAsnsParams) ([]dbq.Asn, error)
	CountAsns(ctx context.Context, arg dbq.CountAsnsParams) (int64, error)
	ListPrefixLists(ctx context.Context, arg dbq.ListPrefixListsParams) ([]dbq.PrefixList, error)
	CountPrefixLists(ctx context.Context, arg dbq.CountPrefixListsParams) (int64, error)
	ListPrefixListEntries(ctx context.Context, arg dbq.ListPrefixListEntriesParams) ([]dbq.PrefixListEntry, error)
	CountPrefixListEntries(ctx context.Context, arg dbq.CountPrefixListEntriesParams) (int64, error)

	ListCommunityLists(ctx context.Context, arg dbq.ListCommunityListsParams) ([]dbq.CommunityList, error)
	CountCommunityLists(ctx context.Context, arg dbq.CountCommunityListsParams) (int64, error)
	ListCommunityListEntries(ctx context.Context, arg dbq.ListCommunityListEntriesParams) ([]dbq.CommunityListEntry, error)
	CountCommunityListEntries(ctx context.Context, arg dbq.CountCommunityListEntriesParams) (int64, error)
	ListRouteMaps(ctx context.Context, arg dbq.ListRouteMapsParams) ([]dbq.RouteMap, error)
	CountRouteMaps(ctx context.Context) (int64, error)
	ListRouteMapEntries(ctx context.Context, arg dbq.ListRouteMapEntriesParams) ([]dbq.RouteMapEntry, error)
	CountRouteMapEntries(ctx context.Context, arg dbq.CountRouteMapEntriesParams) (int64, error)

	// Mutations (PR 44). TCP AO + asn bulk-rotate deferred.
	CreateAsn(ctx context.Context, arg dbq.CreateAsnParams) (dbq.Asn, error)
	UpdateAsn(ctx context.Context, arg dbq.UpdateAsnParams) (dbq.Asn, error)
	DeleteAsn(ctx context.Context, id uuid.UUID) error
	CreatePrefixList(ctx context.Context, arg dbq.CreatePrefixListParams) (dbq.PrefixList, error)
	UpdatePrefixList(ctx context.Context, arg dbq.UpdatePrefixListParams) (dbq.PrefixList, error)
	DeletePrefixList(ctx context.Context, id uuid.UUID) error
	CreatePrefixListEntry(ctx context.Context, arg dbq.CreatePrefixListEntryParams) (dbq.PrefixListEntry, error)
	UpdatePrefixListEntry(ctx context.Context, arg dbq.UpdatePrefixListEntryParams) (dbq.PrefixListEntry, error)
	DeletePrefixListEntry(ctx context.Context, id uuid.UUID) error
	CreateCommunityList(ctx context.Context, arg dbq.CreateCommunityListParams) (dbq.CommunityList, error)
	UpdateCommunityList(ctx context.Context, arg dbq.UpdateCommunityListParams) (dbq.CommunityList, error)
	DeleteCommunityList(ctx context.Context, id uuid.UUID) error
	CreateCommunityListEntry(ctx context.Context, arg dbq.CreateCommunityListEntryParams) (dbq.CommunityListEntry, error)
	UpdateCommunityListEntry(ctx context.Context, arg dbq.UpdateCommunityListEntryParams) (dbq.CommunityListEntry, error)
	DeleteCommunityListEntry(ctx context.Context, id uuid.UUID) error
	CreateRouteMap(ctx context.Context, arg dbq.CreateRouteMapParams) (dbq.RouteMap, error)
	UpdateRouteMap(ctx context.Context, arg dbq.UpdateRouteMapParams) (dbq.RouteMap, error)
	DeleteRouteMap(ctx context.Context, id uuid.UUID) error
	CreateRouteMapEntry(ctx context.Context, arg dbq.CreateRouteMapEntryParams) (dbq.RouteMapEntry, error)
	UpdateRouteMapEntry(ctx context.Context, arg dbq.UpdateRouteMapEntryParams) (dbq.RouteMapEntry, error)
	DeleteRouteMapEntry(ctx context.Context, id uuid.UUID) error
}

type Handler struct {
	Q     Querier
	Audit audit.Recorder
}

func (h *Handler) Mount(r chi.Router) {
	r.Route("/bgp", func(r chi.Router) {
		r.Get("/asns", h.listAsns)
		r.Get("/prefix-lists", h.listPrefixLists)
		r.Get("/prefix-list-entries", h.listPrefixListEntries)
		r.Get("/community-lists", h.listCommunityLists)
		r.Get("/community-list-entries", h.listCommunityListEntries)
		r.Get("/route-maps", h.listRouteMaps)
		r.Get("/route-map-entries", h.listRouteMapEntries)

		// ---- Mutations (PR 44) ----
		r.With(auth.RequireCapability("routing:asns:create")).Post("/asns", h.createAsn)
		r.With(auth.RequireCapability("routing:asns:update")).Patch("/asns/{id}", h.updateAsn)
		r.With(auth.RequireCapability("routing:asns:delete")).Delete("/asns/{id}", h.deleteAsn)
		r.With(auth.RequireCapability("routing:prefix-lists:create")).Post("/prefix-lists", h.createPrefixList)
		r.With(auth.RequireCapability("routing:prefix-lists:update")).Patch("/prefix-lists/{id}", h.updatePrefixList)
		r.With(auth.RequireCapability("routing:prefix-lists:delete")).Delete("/prefix-lists/{id}", h.deletePrefixList)
		r.With(auth.RequireCapability("routing:prefix-list-entries:create")).Post("/prefix-list-entries", h.createPrefixListEntry)
		r.With(auth.RequireCapability("routing:prefix-list-entries:update")).Patch("/prefix-list-entries/{id}", h.updatePrefixListEntry)
		r.With(auth.RequireCapability("routing:prefix-list-entries:delete")).Delete("/prefix-list-entries/{id}", h.deletePrefixListEntry)
		r.With(auth.RequireCapability("routing:community-lists:create")).Post("/community-lists", h.createCommunityList)
		r.With(auth.RequireCapability("routing:community-lists:update")).Patch("/community-lists/{id}", h.updateCommunityList)
		r.With(auth.RequireCapability("routing:community-lists:delete")).Delete("/community-lists/{id}", h.deleteCommunityList)
		r.With(auth.RequireCapability("routing:community-list-entries:create")).Post("/community-list-entries", h.createCommunityListEntry)
		r.With(auth.RequireCapability("routing:community-list-entries:update")).Patch("/community-list-entries/{id}", h.updateCommunityListEntry)
		r.With(auth.RequireCapability("routing:community-list-entries:delete")).Delete("/community-list-entries/{id}", h.deleteCommunityListEntry)
		r.With(auth.RequireCapability("routing:route-maps:create")).Post("/route-maps", h.createRouteMap)
		r.With(auth.RequireCapability("routing:route-maps:update")).Patch("/route-maps/{id}", h.updateRouteMap)
		r.With(auth.RequireCapability("routing:route-maps:delete")).Delete("/route-maps/{id}", h.deleteRouteMap)
		r.With(auth.RequireCapability("routing:route-map-entries:create")).Post("/route-map-entries", h.createRouteMapEntry)
		r.With(auth.RequireCapability("routing:route-map-entries:update")).Patch("/route-map-entries/{id}", h.updateRouteMapEntry)
		r.With(auth.RequireCapability("routing:route-map-entries:delete")).Delete("/route-map-entries/{id}", h.deleteRouteMapEntry)
	})
}

type asnsPage struct {
	Items  []dbq.Asn `json:"items"`
	Total  int64     `json:"total"`
	Limit  int32     `json:"limit"`
	Offset int32     `json:"offset"`
}

func (h *Handler) listAsns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
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
	limit, offset := httpx.PageBounds(q)
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
	limit, offset := httpx.PageBounds(q)
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

// ---- Community lists ----

type communityListsPage struct {
	Items  []dbq.CommunityList `json:"items"`
	Total  int64               `json:"total"`
	Limit  int32               `json:"limit"`
	Offset int32               `json:"offset"`
}

func (h *Handler) listCommunityLists(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	params := dbq.ListCommunityListsParams{Limit: limit, Offset: offset, Kind: strPtr(q.Get("kind"))}
	items, err := h.Q.ListCommunityLists(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountCommunityLists(r.Context(), dbq.CountCommunityListsParams{Kind: params.Kind})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, communityListsPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

// ---- Community list entries ----

type communityListEntriesPage struct {
	Items  []dbq.CommunityListEntry `json:"items"`
	Total  int64                    `json:"total"`
	Limit  int32                    `json:"limit"`
	Offset int32                    `json:"offset"`
}

func (h *Handler) listCommunityListEntries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	params := dbq.ListCommunityListEntriesParams{Limit: limit, Offset: offset}
	if v := q.Get("community_list_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "community_list_id is not a uuid")
			return
		}
		params.CommunityListID = &id
	}
	items, err := h.Q.ListCommunityListEntries(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountCommunityListEntries(r.Context(), dbq.CountCommunityListEntriesParams{CommunityListID: params.CommunityListID})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, communityListEntriesPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

// ---- Route maps ----

type routeMapsPage struct {
	Items  []dbq.RouteMap `json:"items"`
	Total  int64          `json:"total"`
	Limit  int32          `json:"limit"`
	Offset int32          `json:"offset"`
}

func (h *Handler) listRouteMaps(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	items, err := h.Q.ListRouteMaps(r.Context(), dbq.ListRouteMapsParams{Limit: limit, Offset: offset})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountRouteMaps(r.Context())
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, routeMapsPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

// ---- Route map entries ----

type routeMapEntriesPage struct {
	Items  []dbq.RouteMapEntry `json:"items"`
	Total  int64               `json:"total"`
	Limit  int32               `json:"limit"`
	Offset int32               `json:"offset"`
}

func (h *Handler) listRouteMapEntries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	params := dbq.ListRouteMapEntriesParams{Limit: limit, Offset: offset}
	if v := q.Get("route_map_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "route_map_id is not a uuid")
			return
		}
		params.RouteMapID = &id
	}
	items, err := h.Q.ListRouteMapEntries(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountRouteMapEntries(r.Context(), dbq.CountRouteMapEntriesParams{RouteMapID: params.RouteMapID})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, routeMapEntriesPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
