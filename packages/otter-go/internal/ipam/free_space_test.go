// PR 65 — handler tests for GET /ipam/free-space/in-subnets.
// Pure-ish: a fake Querier returns canned subnet + address rows
// so the handler's family filter, min_free threshold, descending
// sort, and limit cap can be pinned without a live DB.
package ipam

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// fakeFreeSpaceQ stubs the two queries the handler needs and lets
// each test inject specific rows. Embeds fakeQ so the rest of the
// Querier interface stays covered by the broader handler test's
// no-op defaults.
type fakeFreeSpaceQ struct {
	fakeQ
	subnets   []dbq.SubnetForFreeSpaceRow
	addresses []dbq.AddressInSubnetRow
	gotParams dbq.ListSubnetsForFreeSpaceParams
	gotIDs    []uuid.UUID
}

func (f *fakeFreeSpaceQ) ListSubnetsForFreeSpace(_ context.Context, arg dbq.ListSubnetsForFreeSpaceParams) ([]dbq.SubnetForFreeSpaceRow, error) {
	f.gotParams = arg
	return f.subnets, nil
}

func (f *fakeFreeSpaceQ) ListAddressesInSubnets(_ context.Context, ids []uuid.UUID) ([]dbq.AddressInSubnetRow, error) {
	f.gotIDs = ids
	out := []dbq.AddressInSubnetRow{}
	want := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	for _, a := range f.addresses {
		if _, ok := want[a.SubnetID]; ok {
			out = append(out, a)
		}
	}
	return out, nil
}

func mountFreeSpace(f *fakeFreeSpaceQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}

// Helper: build a SubnetForFreeSpaceRow with the bare-minimum fields
// the response cares about.
func sub(id uuid.UUID, prefix string) dbq.SubnetForFreeSpaceRow {
	return dbq.SubnetForFreeSpaceRow{
		ID: id, FabricID: uuid.New(), VrfID: uuid.New(),
		Prefix: prefix,
	}
}

func TestFreeSpaceInSubnets_SortsByFreeDescending(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	f := &fakeFreeSpaceQ{
		subnets: []dbq.SubnetForFreeSpaceRow{
			sub(a, "10.0.0.0/30"),  // capacity 2, allocated 1, free 1
			sub(b, "10.0.1.0/24"),  // capacity 254, allocated 0, free 254
			sub(c, "10.0.2.0/28"),  // capacity 14, allocated 2, free 12
		},
		addresses: []dbq.AddressInSubnetRow{
			{SubnetID: a, Address: "10.0.0.1"},
			{SubnetID: c, Address: "10.0.2.1"},
			{SubnetID: c, Address: "10.0.2.2"},
		},
	}
	srv := httptest.NewServer(mountFreeSpace(f))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/ipam/free-space/in-subnets")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out freeSpaceInSubnetsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Count != 3 {
		t.Errorf("Count = %d, want 3", out.Count)
	}
	if out.Subnets[0].Prefix != "10.0.1.0/24" || out.Subnets[1].Prefix != "10.0.2.0/28" || out.Subnets[2].Prefix != "10.0.0.0/30" {
		t.Errorf("ordering wrong: %+v", out.Subnets)
	}
	if out.Subnets[0].Free != 254 || out.Subnets[1].Free != 12 || out.Subnets[2].Free != 1 {
		t.Errorf("free counts wrong: %+v", out.Subnets)
	}
}

func TestFreeSpaceInSubnets_FilterByFamily(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	f := &fakeFreeSpaceQ{
		subnets: []dbq.SubnetForFreeSpaceRow{
			sub(a, "10.0.0.0/24"),
			sub(b, "2001:db8::/64"),
		},
	}
	srv := httptest.NewServer(mountFreeSpace(f))
	defer srv.Close()
	// v4 only
	resp, _ := http.Get(srv.URL + "/ipam/free-space/in-subnets?family=v4")
	var out freeSpaceInSubnetsResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out.Count != 1 || out.Subnets[0].Prefix != "10.0.0.0/24" {
		t.Errorf("family=v4: got %+v", out)
	}
	// v6 only
	resp, _ = http.Get(srv.URL + "/ipam/free-space/in-subnets?family=v6")
	out = freeSpaceInSubnetsResponse{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out.Count != 1 || out.Subnets[0].Prefix != "2001:db8::/64" {
		t.Errorf("family=v6: got %+v", out)
	}
}

func TestFreeSpaceInSubnets_MinFreeThreshold(t *testing.T) {
	// min_free=100 should drop the /30 (free=2) and keep the /24 (free=254).
	a, b := uuid.New(), uuid.New()
	f := &fakeFreeSpaceQ{
		subnets: []dbq.SubnetForFreeSpaceRow{
			sub(a, "10.0.0.0/30"),
			sub(b, "10.0.1.0/24"),
		},
	}
	srv := httptest.NewServer(mountFreeSpace(f))
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/ipam/free-space/in-subnets?min_free=100")
	var out freeSpaceInSubnetsResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out.Count != 1 || out.Subnets[0].Prefix != "10.0.1.0/24" {
		t.Errorf("min_free=100 should keep only the /24: got %+v", out)
	}
}

func TestFreeSpaceInSubnets_LimitCapsResponse(t *testing.T) {
	// Three /24s, limit=2 → 2 rows. Bigger subnets first (sort
	// happens before the limit cut).
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	f := &fakeFreeSpaceQ{
		subnets: []dbq.SubnetForFreeSpaceRow{
			sub(a, "10.0.0.0/30"),  // free 2
			sub(b, "10.0.1.0/24"),  // free 254
			sub(c, "10.0.2.0/28"),  // free 14
		},
	}
	srv := httptest.NewServer(mountFreeSpace(f))
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/ipam/free-space/in-subnets?limit=2")
	var out freeSpaceInSubnetsResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out.Count != 2 {
		t.Errorf("Count = %d, want 2", out.Count)
	}
	if out.Subnets[0].Prefix != "10.0.1.0/24" || out.Subnets[1].Prefix != "10.0.2.0/28" {
		t.Errorf("limit cut wrong rows: %+v", out.Subnets)
	}
}

func TestFreeSpaceInSubnets_FabricVrfFiltersPassToDB(t *testing.T) {
	// Verify the handler forwards fabric_id / vrf_id to the query.
	fab, vrf := uuid.New(), uuid.New()
	f := &fakeFreeSpaceQ{}
	srv := httptest.NewServer(mountFreeSpace(f))
	defer srv.Close()
	url := srv.URL + "/ipam/free-space/in-subnets?fabric_id=" + fab.String() + "&vrf_id=" + vrf.String()
	resp, _ := http.Get(url)
	resp.Body.Close()
	if f.gotParams.FabricID == nil || *f.gotParams.FabricID != fab {
		t.Errorf("fabric_id not forwarded: %+v", f.gotParams)
	}
	if f.gotParams.VrfID == nil || *f.gotParams.VrfID != vrf {
		t.Errorf("vrf_id not forwarded: %+v", f.gotParams)
	}
}

func TestFreeSpaceInSubnets_BadFabricIDIsRejected(t *testing.T) {
	srv := httptest.NewServer(mountFreeSpace(&fakeFreeSpaceQ{}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/ipam/free-space/in-subnets?fabric_id=not-a-uuid")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestFreeSpaceInSubnets_BadFamilyIsRejected(t *testing.T) {
	srv := httptest.NewServer(mountFreeSpace(&fakeFreeSpaceQ{}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/ipam/free-space/in-subnets?family=v7")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for unknown family", resp.StatusCode)
	}
}

func TestFreeSpaceInSubnets_EchoesQueryFields(t *testing.T) {
	// The Python response includes the request params under "query".
	// Pin the echo so the frontend's display logic doesn't break.
	fab, vrf := uuid.New(), uuid.New()
	f := &fakeFreeSpaceQ{}
	srv := httptest.NewServer(mountFreeSpace(f))
	defer srv.Close()
	url := srv.URL + "/ipam/free-space/in-subnets?fabric_id=" + fab.String() + "&vrf_id=" + vrf.String() + "&family=v4&min_free=5"
	resp, _ := http.Get(url)
	var out freeSpaceInSubnetsResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out.Query.FabricID == nil || *out.Query.FabricID != fab.String() {
		t.Errorf("query.fabric_id = %v", out.Query.FabricID)
	}
	if out.Query.VrfID == nil || *out.Query.VrfID != vrf.String() {
		t.Errorf("query.vrf_id = %v", out.Query.VrfID)
	}
	if out.Query.Family == nil || *out.Query.Family != "v4" {
		t.Errorf("query.family = %v", out.Query.Family)
	}
	if out.Query.MinFree != 5 {
		t.Errorf("query.min_free = %d, want 5", out.Query.MinFree)
	}
}

func TestFreeSpaceInSubnets_SkipsUnparseablePrefixes(t *testing.T) {
	// A bad prefix on a subnet row shouldn't tank the whole scan.
	a, b := uuid.New(), uuid.New()
	f := &fakeFreeSpaceQ{
		subnets: []dbq.SubnetForFreeSpaceRow{
			sub(a, "garbage"),
			sub(b, "10.0.1.0/24"),
		},
	}
	srv := httptest.NewServer(mountFreeSpace(f))
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/ipam/free-space/in-subnets")
	var out freeSpaceInSubnetsResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out.Count != 1 || out.Subnets[0].Prefix != "10.0.1.0/24" {
		t.Errorf("bad row should be skipped: %+v", out)
	}
}

func TestFreeSpaceInSubnets_IncludesNextAvailable(t *testing.T) {
	a := uuid.New()
	f := &fakeFreeSpaceQ{
		subnets: []dbq.SubnetForFreeSpaceRow{sub(a, "10.0.0.0/24")},
		addresses: []dbq.AddressInSubnetRow{
			{SubnetID: a, Address: "10.0.0.1"},
		},
	}
	srv := httptest.NewServer(mountFreeSpace(f))
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/ipam/free-space/in-subnets")
	var out freeSpaceInSubnetsResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out.Subnets[0].NextAvailable == nil || *out.Subnets[0].NextAvailable != "10.0.0.2" {
		t.Errorf("NextAvailable = %v, want 10.0.0.2", out.Subnets[0].NextAvailable)
	}
}
