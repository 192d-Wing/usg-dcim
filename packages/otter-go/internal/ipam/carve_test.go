// PR 66 — unit + handler tests for the CIDR carver. Unit tests
// pin the prefix-overlap predicate + the supernet sub-iteration;
// handler tests pin family filter, family-bound size check
// (asking /24 inside a /48 v6 parent is no good), grouped
// response shape, and limit_per_supernet.
package ipam

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

func mustPrefix(s string) netip.Prefix {
	p, err := netip.ParsePrefix(s)
	if err != nil {
		panic(err)
	}
	return p
}

func TestPrefixesOverlap(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"10.0.0.0/24", "10.0.0.0/26", true},     // child of a
		{"10.0.0.0/24", "10.0.0.128/25", true},   // child of a
		{"10.0.0.0/24", "10.0.1.0/24", false},    // disjoint siblings
		{"10.0.0.0/16", "10.0.0.0/24", true},     // a contains b
		{"10.0.0.0/16", "10.1.0.0/16", false},    // disjoint
		{"2001:db8::/64", "2001:db8::/96", true}, // v6 containment
		{"2001:db8::/64", "2001:db9::/64", false}, // v6 disjoint
	}
	for _, c := range cases {
		got := prefixesOverlap(mustPrefix(c.a), mustPrefix(c.b))
		if got != c.want {
			t.Errorf("prefixesOverlap(%s, %s) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestFindFreePrefixesInSupernet_NoOverlap(t *testing.T) {
	// /22 contains four /24s. With nothing allocated, all four are
	// candidates; limit=2 stops after the first two.
	parent := mustPrefix("10.0.0.0/22")
	out := findFreePrefixesInSupernet(parent, 24, nil, 2)
	if len(out) != 2 {
		t.Fatalf("want 2 candidates, got %d (%v)", len(out), out)
	}
	if out[0] != "10.0.0.0/24" || out[1] != "10.0.1.0/24" {
		t.Errorf("got %v, want 10.0.0.0/24, 10.0.1.0/24", out)
	}
}

func TestFindFreePrefixesInSupernet_SkipsOverlappingAllocations(t *testing.T) {
	parent := mustPrefix("10.0.0.0/22")
	occupied := []netip.Prefix{mustPrefix("10.0.0.0/24"), mustPrefix("10.0.2.0/24")}
	out := findFreePrefixesInSupernet(parent, 24, occupied, 10)
	if len(out) != 2 {
		t.Fatalf("want 2 (the unallocated /24s), got %d (%v)", len(out), out)
	}
	if out[0] != "10.0.1.0/24" || out[1] != "10.0.3.0/24" {
		t.Errorf("got %v, want the two unallocated /24s", out)
	}
}

func TestFindFreePrefixesInSupernet_RejectsBadSize(t *testing.T) {
	parent := mustPrefix("10.0.0.0/24")
	// Asking for a /16 inside a /24 — size <= parent.Bits.
	if got := findFreePrefixesInSupernet(parent, 16, nil, 10); got != nil {
		t.Errorf("expected nil for size <= parent, got %v", got)
	}
	// Asking for /40 inside a v4 /24 — past family max.
	if got := findFreePrefixesInSupernet(parent, 40, nil, 10); got != nil {
		t.Errorf("expected nil for size > family max, got %v", got)
	}
}

func TestFindFreePrefixesInSupernet_V6(t *testing.T) {
	// /48 parent, /50 target → 4 candidates with nothing allocated.
	parent := mustPrefix("2001:db8::/48")
	out := findFreePrefixesInSupernet(parent, 50, nil, 10)
	if len(out) != 4 {
		t.Fatalf("want 4 candidates, got %d (%v)", len(out), out)
	}
	want := []string{
		"2001:db8::/50",
		"2001:db8:0:4000::/50",
		"2001:db8:0:8000::/50",
		"2001:db8:0:c000::/50",
	}
	for i, w := range want {
		if out[i] != w {
			t.Errorf("out[%d] = %s, want %s", i, out[i], w)
		}
	}
}

func TestFindFreePrefixesInSupernet_IgnoresCrossFamilyAllocations(t *testing.T) {
	// A v4 supernet shouldn't be confused by an unrelated v6
	// allocation in the input list.
	parent := mustPrefix("10.0.0.0/22")
	occupied := []netip.Prefix{mustPrefix("2001:db8::/64")}
	out := findFreePrefixesInSupernet(parent, 24, occupied, 4)
	if len(out) != 4 {
		t.Errorf("want 4 (v6 row ignored), got %d (%v)", len(out), out)
	}
}

// ---- Handler ----

type fakeCarverQ struct {
	fakeQ
	supernets []dbq.SupernetForCarverRow
	allocated []dbq.SubnetPrefixBySupernetRow
	gotParams dbq.ListSupernetsForCarverParams
	gotIDs    []uuid.UUID
}

func (f *fakeCarverQ) ListSupernetsForCarver(_ context.Context, arg dbq.ListSupernetsForCarverParams) ([]dbq.SupernetForCarverRow, error) {
	f.gotParams = arg
	return f.supernets, nil
}

func (f *fakeCarverQ) ListSubnetPrefixesBySupernets(_ context.Context, ids []uuid.UUID) ([]dbq.SubnetPrefixBySupernetRow, error) {
	f.gotIDs = ids
	want := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	out := []dbq.SubnetPrefixBySupernetRow{}
	for _, a := range f.allocated {
		if _, ok := want[a.SupernetID]; ok {
			out = append(out, a)
		}
	}
	return out, nil
}

func mountCarver(f *fakeCarverQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}

func TestFreeSpacePrefixes_HappyPath(t *testing.T) {
	a := uuid.New()
	f := &fakeCarverQ{
		supernets: []dbq.SupernetForCarverRow{
			{ID: a, FabricID: uuid.New(), VrfID: uuid.New(), Prefix: "10.0.0.0/22"},
		},
		allocated: []dbq.SubnetPrefixBySupernetRow{
			{SupernetID: a, Prefix: "10.0.0.0/24"},
		},
	}
	srv := httptest.NewServer(mountCarver(f))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/ipam/free-space/prefixes?prefix_size=24")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out freeSpacePrefixResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Supernets) != 1 {
		t.Fatalf("want 1 supernet group, got %d", len(out.Supernets))
	}
	g := out.Supernets[0]
	if g.Count != 3 {
		t.Errorf("Count = %d, want 3 (4 /24s minus 1 allocated)", g.Count)
	}
	if g.Candidates[0] != "10.0.1.0/24" {
		t.Errorf("first candidate = %s, want 10.0.1.0/24", g.Candidates[0])
	}
}

func TestFreeSpacePrefixes_RequiresPrefixSize(t *testing.T) {
	srv := httptest.NewServer(mountCarver(&fakeCarverQ{}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/ipam/free-space/prefixes")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestFreeSpacePrefixes_FamilyFilter(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	f := &fakeCarverQ{
		supernets: []dbq.SupernetForCarverRow{
			{ID: a, FabricID: uuid.New(), VrfID: uuid.New(), Prefix: "10.0.0.0/22"},
			{ID: b, FabricID: uuid.New(), VrfID: uuid.New(), Prefix: "2001:db8::/48"},
		},
	}
	srv := httptest.NewServer(mountCarver(f))
	defer srv.Close()
	// v4 only — should carve from the /22 only.
	resp, _ := http.Get(srv.URL + "/ipam/free-space/prefixes?prefix_size=24&family=v4")
	var out freeSpacePrefixResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if len(out.Supernets) != 1 || out.Supernets[0].SupernetPrefix != "10.0.0.0/22" {
		t.Errorf("family=v4 should pick v4 supernet only: %+v", out)
	}
}

func TestFreeSpacePrefixes_PrefixSizeBeyondFamilyMaxSkips(t *testing.T) {
	// prefix_size=64 against a v4 supernet — 64 > 32 → skip; against
	// the v6 supernet — fine.
	a, b := uuid.New(), uuid.New()
	f := &fakeCarverQ{
		supernets: []dbq.SupernetForCarverRow{
			{ID: a, FabricID: uuid.New(), VrfID: uuid.New(), Prefix: "10.0.0.0/22"},
			{ID: b, FabricID: uuid.New(), VrfID: uuid.New(), Prefix: "2001:db8::/48"},
		},
	}
	srv := httptest.NewServer(mountCarver(f))
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/ipam/free-space/prefixes?prefix_size=64")
	var out freeSpacePrefixResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if len(out.Supernets) != 1 || out.Supernets[0].SupernetPrefix != "2001:db8::/48" {
		t.Errorf("/64 ask should skip v4 supernet: %+v", out)
	}
}

func TestFreeSpacePrefixes_LimitPerSupernet(t *testing.T) {
	a := uuid.New()
	f := &fakeCarverQ{
		supernets: []dbq.SupernetForCarverRow{
			{ID: a, FabricID: uuid.New(), VrfID: uuid.New(), Prefix: "10.0.0.0/22"},
		},
	}
	srv := httptest.NewServer(mountCarver(f))
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/ipam/free-space/prefixes?prefix_size=24&limit_per_supernet=2")
	var out freeSpacePrefixResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out.Supernets[0].Count != 2 {
		t.Errorf("Count = %d, want 2 (limit_per_supernet)", out.Supernets[0].Count)
	}
}

func TestFreeSpacePrefixes_OmitsFullSupernets(t *testing.T) {
	// /23 with two /24s allocated → zero candidates → drop the
	// whole group from the response.
	a := uuid.New()
	f := &fakeCarverQ{
		supernets: []dbq.SupernetForCarverRow{
			{ID: a, FabricID: uuid.New(), VrfID: uuid.New(), Prefix: "10.0.0.0/23"},
		},
		allocated: []dbq.SubnetPrefixBySupernetRow{
			{SupernetID: a, Prefix: "10.0.0.0/24"},
			{SupernetID: a, Prefix: "10.0.1.0/24"},
		},
	}
	srv := httptest.NewServer(mountCarver(f))
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/ipam/free-space/prefixes?prefix_size=24")
	var out freeSpacePrefixResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if len(out.Supernets) != 0 {
		t.Errorf("full supernet should be omitted: %+v", out)
	}
}

func TestFreeSpacePrefixes_EchoesQueryFields(t *testing.T) {
	fab, vrf, sup := uuid.New(), uuid.New(), uuid.New()
	srv := httptest.NewServer(mountCarver(&fakeCarverQ{}))
	defer srv.Close()
	url := srv.URL + "/ipam/free-space/prefixes?prefix_size=24&fabric_id=" + fab.String() +
		"&vrf_id=" + vrf.String() + "&supernet_id=" + sup.String() + "&family=v4"
	resp, _ := http.Get(url)
	var out freeSpacePrefixResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out.Query.PrefixSize != 24 {
		t.Errorf("query.prefix_size = %d", out.Query.PrefixSize)
	}
	if out.Query.FabricID == nil || *out.Query.FabricID != fab.String() {
		t.Errorf("query.fabric_id wrong: %v", out.Query.FabricID)
	}
	if out.Query.VrfID == nil || *out.Query.VrfID != vrf.String() {
		t.Errorf("query.vrf_id wrong: %v", out.Query.VrfID)
	}
	if out.Query.SupernetID == nil || *out.Query.SupernetID != sup.String() {
		t.Errorf("query.supernet_id wrong: %v", out.Query.SupernetID)
	}
	if out.Query.Family == nil || *out.Query.Family != "v4" {
		t.Errorf("query.family wrong: %v", out.Query.Family)
	}
}
