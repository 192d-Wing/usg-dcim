// Free-space discovery endpoints (PR 65+). Ports the capacity-
// planning views from packages/otter Python (api/ipam.py
// free_space_in_subnets / free_space_prefixes). The "in-subnets"
// endpoint answers "where can I place N more hosts?"; the
// "prefixes" endpoint answers "find me a /24 inside this supernet
// that isn't already a subnet."
//
// ABAC parity note: the Python handlers require
// ipam:subnets:read / ipam:supernets:read but don't apply
// scope_filtered_fabric_ids. To preserve cross-backend behavior
// the Go ports match that exactly — if/when Python tightens the
// scope filter, port the same change here.
package ipam

import (
	"net/http"
	"net/netip"
	"sort"
	"strings"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// freeSpaceSubnetRow mirrors the per-subnet entries in the Python
// /free-space/in-subnets response. NextAvailable is nullable —
// distinguishes "subnet is full" (null) from a missing field.
type freeSpaceSubnetRow struct {
	SubnetID      string  `json:"subnet_id"`
	Prefix        string  `json:"prefix"`
	Name          *string `json:"name"`
	SiteID        *string `json:"site_id"`
	FabricID      string  `json:"fabric_id"`
	VrfID         string  `json:"vrf_id"`
	Purpose       *string `json:"purpose"`
	Capacity      int64   `json:"capacity"`
	Allocated     int     `json:"allocated"`
	Free          int64   `json:"free"`
	NextAvailable *string `json:"next_available"`
}

type freeSpaceInSubnetsQuery struct {
	FabricID *string `json:"fabric_id"`
	VrfID    *string `json:"vrf_id"`
	Family   *string `json:"family"`
	MinFree  int64   `json:"min_free"`
}

type freeSpaceInSubnetsResponse struct {
	Query   freeSpaceInSubnetsQuery `json:"query"`
	Subnets []freeSpaceSubnetRow    `json:"subnets"`
	Count   int                     `json:"count"`
}

// familyMatches: colon ⇒ v6, no colon ⇒ v4. Cheap inline check;
// matches the Python _supernet_matches_family helper.
func familyMatches(prefix string, family string) bool {
	if family == "" {
		return true
	}
	isV4 := !strings.Contains(prefix, ":")
	if family == "v4" {
		return isV4
	}
	if family == "v6" {
		return !isV4
	}
	return true
}

func (h *Handler) getFreeSpaceInSubnets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	params := dbq.ListSubnetsForFreeSpaceParams{}
	if v := q.Get("fabric_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "fabric_id is not a uuid")
			return
		}
		params.FabricID = &id
	}
	if v := q.Get("vrf_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "vrf_id is not a uuid")
			return
		}
		params.VrfID = &id
	}
	family := q.Get("family")
	if family != "" && family != "v4" && family != "v6" {
		httpx.Error(w, http.StatusBadRequest, "family must be v4 or v6")
		return
	}
	minFree := int64(parseInt32(q.Get("min_free"), 1, 1, 1_000_000_000))
	limit := parseInt32(q.Get("limit"), 50, 1, 500)

	subnets, err := h.Q.ListSubnetsForFreeSpace(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	// Family pre-filter — done before the address fetch so we don't
	// pull addresses for subnets we'll drop anyway.
	filtered := make([]dbq.ListSubnetsForFreeSpaceRow, 0, len(subnets))
	for _, s := range subnets {
		if familyMatches(s.Prefix, family) {
			filtered = append(filtered, s)
		}
	}
	// Bulk address fetch — one round-trip instead of N+1. This is
	// the Go-vs-Python performance win for tenants with many subnets.
	ids := make([]uuid.UUID, len(filtered))
	for i, s := range filtered {
		ids[i] = s.ID
	}
	addrRows, err := h.Q.ListAddressesInSubnets(r.Context(), ids)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	used := make(map[uuid.UUID][]string, len(filtered))
	for _, a := range addrRows {
		used[a.SubnetID] = append(used[a.SubnetID], a.Address)
	}
	out := make([]freeSpaceSubnetRow, 0, len(filtered))
	for _, s := range filtered {
		capacity, err := networkCapacity(s.Prefix)
		if err != nil {
			// Bad prefix on a subnet row — skip rather than fail the
			// whole capacity scan (consistent with PR 63's policy).
			continue
		}
		u := used[s.ID]
		free := capacity - int64(len(u))
		if free < 0 {
			free = 0
		}
		if free < minFree {
			continue
		}
		var siteID *string
		if s.SiteID != nil {
			v := s.SiteID.String()
			siteID = &v
		}
		out = append(out, freeSpaceSubnetRow{
			SubnetID:      s.ID.String(),
			Prefix:        s.Prefix,
			Name:          s.Name,
			SiteID:        siteID,
			FabricID:      s.FabricID.String(),
			VrfID:         s.VrfID.String(),
			Purpose:       s.Purpose,
			Capacity:      capacity,
			Allocated:     len(u),
			Free:          free,
			NextAvailable: nextFreeAddress(s.Prefix, u),
		})
	}
	// Sort descending by free (Python: out.sort(key=lambda r: -r["free"])).
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Free > out[j].Free
	})
	if int(limit) < len(out) {
		out = out[:limit]
	}
	// Query echo for response — matches Python's response shape so
	// the frontend can use either backend interchangeably.
	resp := freeSpaceInSubnetsResponse{
		Query:   freeSpaceInSubnetsQuery{Family: optStr(family), MinFree: minFree},
		Subnets: out,
		Count:   len(out),
	}
	if params.FabricID != nil {
		s := params.FabricID.String()
		resp.Query.FabricID = &s
	}
	if params.VrfID != nil {
		s := params.VrfID.String()
		resp.Query.VrfID = &s
	}
	httpx.JSON(w, http.StatusOK, resp)
}

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ---- PR 66: /ipam/free-space/prefixes ----

type freeSpacePrefixCandidate struct {
	SupernetID     string   `json:"supernet_id"`
	SupernetPrefix string   `json:"supernet_prefix"`
	SupernetName   *string  `json:"supernet_name"`
	FabricID       string   `json:"fabric_id"`
	VrfID          string   `json:"vrf_id"`
	Purpose        *string  `json:"purpose"`
	Candidates     []string `json:"candidates"`
	Count          int      `json:"count"`
}

type freeSpacePrefixQuery struct {
	PrefixSize int     `json:"prefix_size"`
	FabricID   *string `json:"fabric_id"`
	VrfID      *string `json:"vrf_id"`
	SupernetID *string `json:"supernet_id"`
	Family     *string `json:"family"`
}

type freeSpacePrefixResponse struct {
	Query     freeSpacePrefixQuery       `json:"query"`
	Supernets []freeSpacePrefixCandidate `json:"supernets"`
}

func (h *Handler) getFreeSpacePrefixes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rawSize := q.Get("prefix_size")
	if rawSize == "" {
		httpx.Error(w, http.StatusBadRequest, "prefix_size is required")
		return
	}
	prefixSize := int(parseInt32(rawSize, 0, 1, 128))
	if prefixSize < 1 || prefixSize > 128 {
		httpx.Error(w, http.StatusBadRequest, "prefix_size must be between 1 and 128")
		return
	}
	params := dbq.ListSupernetsForCarverParams{}
	if v := q.Get("fabric_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "fabric_id is not a uuid")
			return
		}
		params.FabricID = &id
	}
	if v := q.Get("vrf_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "vrf_id is not a uuid")
			return
		}
		params.VrfID = &id
	}
	if v := q.Get("supernet_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "supernet_id is not a uuid")
			return
		}
		params.SupernetID = &id
	}
	family := q.Get("family")
	if family != "" && family != "v4" && family != "v6" {
		httpx.Error(w, http.StatusBadRequest, "family must be v4 or v6")
		return
	}
	limitPerSupernet := parseInt32(q.Get("limit_per_supernet"), 20, 1, 200)

	supernets, err := h.Q.ListSupernetsForCarver(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	// Family pre-filter before the bulk subnet fetch — skip
	// children of supernets we'd drop anyway.
	keep := make([]dbq.ListSupernetsForCarverRow, 0, len(supernets))
	for _, s := range supernets {
		if familyMatches(s.Prefix, family) {
			keep = append(keep, s)
		}
	}
	ids := make([]uuid.UUID, len(keep))
	for i, s := range keep {
		ids[i] = s.ID
	}
	allocRows, err := h.Q.ListSubnetPrefixesBySupernets(r.Context(), ids)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	alloc := make(map[uuid.UUID][]string, len(keep))
	for _, a := range allocRows {
		alloc[a.SupernetID] = append(alloc[a.SupernetID], a.Prefix)
	}

	out := make([]freeSpacePrefixCandidate, 0, len(keep))
	for _, sn := range keep {
		parent, perr := netip.ParsePrefix(sn.Prefix)
		if perr != nil {
			continue
		}
		// Family-bound size: v4 caps at 32, v6 at 128.
		maxSize := 32
		if !parent.Addr().Is4() {
			maxSize = 128
		}
		if prefixSize > maxSize {
			continue
		}
		occupied := make([]netip.Prefix, 0, len(alloc[sn.ID]))
		for _, a := range alloc[sn.ID] {
			if p, err := netip.ParsePrefix(a); err == nil {
				occupied = append(occupied, p)
			}
		}
		cands := findFreePrefixesInSupernet(parent, prefixSize, occupied, int(limitPerSupernet))
		if len(cands) == 0 {
			continue
		}
		out = append(out, freeSpacePrefixCandidate{
			SupernetID:     sn.ID.String(),
			SupernetPrefix: sn.Prefix,
			SupernetName:   sn.Name,
			FabricID:       sn.FabricID.String(),
			VrfID:          sn.VrfID.String(),
			Purpose:        sn.Purpose,
			Candidates:     cands,
			Count:          len(cands),
		})
	}

	resp := freeSpacePrefixResponse{
		Query:     freeSpacePrefixQuery{PrefixSize: prefixSize, Family: optStr(family)},
		Supernets: out,
	}
	if params.FabricID != nil {
		s := params.FabricID.String()
		resp.Query.FabricID = &s
	}
	if params.VrfID != nil {
		s := params.VrfID.String()
		resp.Query.VrfID = &s
	}
	if params.SupernetID != nil {
		s := params.SupernetID.String()
		resp.Query.SupernetID = &s
	}
	httpx.JSON(w, http.StatusOK, resp)
}
