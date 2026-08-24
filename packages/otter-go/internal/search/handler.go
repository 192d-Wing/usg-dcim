// Package search holds the global-search endpoint backing finch's
// header search field. One GET, four buckets (sites, racks, assets,
// IPs); ILIKE substring against the inventory tables, exact host match
// against ip_addresses when the query parses as a literal address.
package search

import (
	"context"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)


type Querier interface {
	SearchSites(ctx context.Context, arg dbq.SearchSitesParams) ([]dbq.SearchSitesRow, error)
	SearchRacks(ctx context.Context, arg dbq.SearchRacksParams) ([]dbq.SearchRacksRow, error)
	SearchAssets(ctx context.Context, arg dbq.SearchAssetsParams) ([]dbq.SearchAssetsRow, error)

	SearchIPAddressesByHost(ctx context.Context, arg dbq.SearchIPAddressesByHostParams) ([]dbq.SearchIPAddressesByHostRow, error)
	SearchSubnetsByIDs(ctx context.Context, ids []uuid.UUID) ([]dbq.SearchSubnetsByIDsRow, error)
	SearchVrfsByIDs(ctx context.Context, ids []uuid.UUID) ([]dbq.SearchVrfsByIDsRow, error)
	SearchFabricsByIDs(ctx context.Context, ids []uuid.UUID) ([]dbq.SearchFabricsByIDsRow, error)
	SearchAssetsByIDs(ctx context.Context, ids []uuid.UUID) ([]dbq.SearchAssetsByIDsRow, error)
}

type Handler struct {
	Q Querier
}

func (h *Handler) Mount(r chi.Router) {
	r.With(auth.RequireCapability("search:search:read")).Get("/search", h.globalSearch)
}

// Response wire shape — mirrors api/search.py's return value
// byte-for-byte. `parsed_ip` is the canonical host string when q
// parses as a literal address (mirrors _looks_like_ip), nil otherwise.
type searchResponse struct {
	Query    string         `json:"query"`
	ParsedIP *string        `json:"parsed_ip"`
	Results  searchBuckets  `json:"results"`
}

type searchBuckets struct {
	Sites  []dbq.SearchSitesRow  `json:"sites"`
	Racks  []dbq.SearchRacksRow  `json:"racks"`
	Assets []dbq.SearchAssetsRow `json:"assets"`
	IPs    []ipResultRow        `json:"ips"`
}

// ipResultRow is the joined IP row finch renders. Python populates the
// subnet_prefix, vrf_name, fabric_name, asset_name fields by walking
// the bulk enrichment maps; we do the same here so the wire shape
// matches exactly.
type ipResultRow struct {
	ID           string  `json:"id"`
	Address      string  `json:"address"`
	Role         string  `json:"role"`
	Status       string  `json:"status"`
	Source       string  `json:"source"`
	DnsName      *string `json:"dns_name"`
	SubnetID     *string `json:"subnet_id"`
	SubnetPrefix *string `json:"subnet_prefix"`
	VrfID        *string `json:"vrf_id"`
	VrfName      *string `json:"vrf_name"`
	FabricID     *string `json:"fabric_id"`
	FabricName   *string `json:"fabric_name"`
	AssetID      *string `json:"asset_id"`
	AssetName    *string `json:"asset_name"`
}

func (h *Handler) globalSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	// Validate q verbatim — no pre-trim — so the length check + pattern
	// match Python's `Query(min_length=2, max_length=128)` byte-for-byte
	// and `pat = f"%{q}%"` keeps the operator's exact whitespace.
	query := q.Get("q")
	if len(query) < 2 || len(query) > 128 {
		httpx.Error(w, http.StatusBadRequest, "q must be 2-128 characters")
		return
	}
	limit := parseLimit(q.Get("limit"))

	pattern := "%" + query + "%"
	ctx := r.Context()

	sites, err := h.Q.SearchSites(ctx, dbq.SearchSitesParams{Pattern: pattern, Limit: limit})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	racks, err := h.Q.SearchRacks(ctx, dbq.SearchRacksParams{Pattern: pattern, Limit: limit})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	assets, err := h.Q.SearchAssets(ctx, dbq.SearchAssetsParams{Pattern: pattern, Limit: limit})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}

	var parsedIP *string
	var ips []ipResultRow
	if canon := canonicalIP(query); canon != "" {
		parsedIP = &canon
		ips, err = h.ipSearch(ctx, canon, limit)
		if err != nil {
			status, msg := httpx.Mapped(err)
			httpx.Error(w, status, msg)
			return
		}
	}

	httpx.JSON(w, http.StatusOK, searchResponse{
		Query:    query,
		ParsedIP: parsedIP,
		Results: searchBuckets{
			Sites:  emptyIfNil(sites),
			Racks:  emptyIfNil(racks),
			Assets: emptyIfNil(assets),
			IPs:    emptyIfNil(ips),
		},
	})
}

// ipSearch resolves a host string into enriched IPAddress rows —
// runs the four bulk-enrichment queries Python's _ip_search does
// (subnets → vrfs/fabrics, plus the asset_id join) sequentially. Each
// fetch+index step is its own helper so the top-level body stays
// linear and the cognitive-complexity gate is happy.
func (h *Handler) ipSearch(ctx context.Context, host string, limit int32) ([]ipResultRow, error) {
	rows, err := h.Q.SearchIPAddressesByHost(ctx, dbq.SearchIPAddressesByHostParams{Host: host, Limit: limit})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	subnetsByID, err := h.loadSubnets(ctx, rows)
	if err != nil {
		return nil, err
	}
	vrfsByID, err := h.loadVrfs(ctx, subnetsByID)
	if err != nil {
		return nil, err
	}
	fabricsByID, err := h.loadFabrics(ctx, subnetsByID)
	if err != nil {
		return nil, err
	}
	assetsByID, err := h.loadAssets(ctx, rows)
	if err != nil {
		return nil, err
	}
	out := make([]ipResultRow, 0, len(rows))
	for _, ip := range rows {
		out = append(out, joinIPRow(ip, subnetsByID, vrfsByID, fabricsByID, assetsByID))
	}
	return out, nil
}

func (h *Handler) loadSubnets(ctx context.Context, rows []dbq.SearchIPAddressesByHostRow) (map[uuid.UUID]dbq.SearchSubnetsByIDsRow, error) {
	ids := uniqueIDsFromRows(len(rows), func(i int) (uuid.UUID, bool) { return rows[i].SubnetID, true })
	subnets, err := h.Q.SearchSubnetsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	return indexBy(subnets, func(s dbq.SearchSubnetsByIDsRow) uuid.UUID { return s.ID }), nil
}

func (h *Handler) loadVrfs(ctx context.Context, subnets map[uuid.UUID]dbq.SearchSubnetsByIDsRow) (map[uuid.UUID]dbq.SearchVrfsByIDsRow, error) {
	ids := uniqueIDsFromMap(subnets, func(s dbq.SearchSubnetsByIDsRow) uuid.UUID { return s.VrfID })
	vrfs, err := h.Q.SearchVrfsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	return indexBy(vrfs, func(v dbq.SearchVrfsByIDsRow) uuid.UUID { return v.ID }), nil
}

func (h *Handler) loadFabrics(ctx context.Context, subnets map[uuid.UUID]dbq.SearchSubnetsByIDsRow) (map[uuid.UUID]dbq.SearchFabricsByIDsRow, error) {
	ids := uniqueIDsFromMap(subnets, func(s dbq.SearchSubnetsByIDsRow) uuid.UUID { return s.FabricID })
	fabrics, err := h.Q.SearchFabricsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	return indexBy(fabrics, func(f dbq.SearchFabricsByIDsRow) uuid.UUID { return f.ID }), nil
}

func (h *Handler) loadAssets(ctx context.Context, rows []dbq.SearchIPAddressesByHostRow) (map[uuid.UUID]dbq.SearchAssetsByIDsRow, error) {
	ids := uniqueIDsFromRows(len(rows), func(i int) (uuid.UUID, bool) {
		if rows[i].AssetID == nil {
			return uuid.UUID{}, false
		}
		return *rows[i].AssetID, true
	})
	assets, err := h.Q.SearchAssetsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	return indexBy(assets, func(a dbq.SearchAssetsByIDsRow) uuid.UUID { return a.ID }), nil
}

// uniqueIDsFromRows pulls (id, present) from n indexed rows, dedupes
// while preserving first-seen order. Plain pre-1.23 Go — no range-func
// iterators since CI is pinned to 1.22.
func uniqueIDsFromRows(n int, get func(i int) (uuid.UUID, bool)) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, n)
	out := make([]uuid.UUID, 0, n)
	for i := 0; i < n; i++ {
		id, ok := get(i)
		if !ok {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func uniqueIDsFromMap[V any](m map[uuid.UUID]V, key func(V) uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(m))
	out := make([]uuid.UUID, 0, len(m))
	for _, v := range m {
		id := key(v)
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func indexBy[T any, K comparable](rows []T, key func(T) K) map[K]T {
	m := make(map[K]T, len(rows))
	for _, r := range rows {
		m[key(r)] = r
	}
	return m
}

// joinIPRow assembles one ipResultRow by walking the enrichment maps.
// subnet → vrf+fabric (via subnet's FKs), asset → name (if attached).
// Mirrors api/search.py::_ip_search_row.
func joinIPRow(
	ip dbq.SearchIPAddressesByHostRow,
	subnets map[uuid.UUID]dbq.SearchSubnetsByIDsRow,
	vrfs map[uuid.UUID]dbq.SearchVrfsByIDsRow,
	fabrics map[uuid.UUID]dbq.SearchFabricsByIDsRow,
	assets map[uuid.UUID]dbq.SearchAssetsByIDsRow,
) ipResultRow {
	row := ipResultRow{
		ID:      ip.ID.String(),
		Address: ip.Address,
		Role:    ip.Role,
		Status:  ip.Status,
		Source:  ip.Source,
		DnsName: ip.DnsName,
	}
	if s, ok := subnets[ip.SubnetID]; ok {
		sid := s.ID.String()
		row.SubnetID = &sid
		row.SubnetPrefix = &s.Prefix
		if v, ok := vrfs[s.VrfID]; ok {
			vid := v.ID.String()
			row.VrfID = &vid
			vname := v.Name
			row.VrfName = &vname
		}
		if f, ok := fabrics[s.FabricID]; ok {
			fid := f.ID.String()
			row.FabricID = &fid
			fname := f.Name
			row.FabricName = &fname
		}
	}
	if ip.AssetID != nil {
		if a, ok := assets[*ip.AssetID]; ok {
			aid := a.ID.String()
			row.AssetID = &aid
			aname := a.Name
			row.AssetName = &aname
		}
	}
	return row
}

// canonicalIP returns the canonical text of q if it parses as an IPv4
// or IPv6 address, else "". Strips a trailing /N so users can paste
// "10.0.0.5/24" and still hit the row for "10.0.0.5". Mirrors
// api/search.py::_looks_like_ip.
func canonicalIP(q string) string {
	raw := strings.TrimSpace(q)
	if idx := strings.Index(raw, "/"); idx >= 0 {
		raw = raw[:idx]
	}
	a, err := netip.ParseAddr(raw)
	if err != nil {
		return ""
	}
	return a.String()
}

// parseLimit mirrors Python's `Query(25, ge=1, le=200)` — default 25,
// clamped [1, 200], non-numeric falls back to default.
func parseLimit(s string) int32 {
	if s == "" {
		return 25
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 25
	}
	if n < 1 {
		return 1
	}
	if n > 200 {
		return 200
	}
	return int32(n)
}

// emptyIfNil ensures `[]` not `null` for the bucket slices — finch's
// response handler maps directly into the bucket arrays and chokes on
// the JSON null form.
func emptyIfNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
