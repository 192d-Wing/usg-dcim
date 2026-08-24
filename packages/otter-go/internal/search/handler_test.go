package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

type fakeQ struct {
	sites  []dbq.SearchSitesRow
	racks  []dbq.SearchRacksRow
	assets []dbq.SearchAssetsRow

	ips         []dbq.SearchIPAddressesByHostRow
	subnets     []dbq.SearchSubnetsByIDsRow
	vrfs        []dbq.SearchVrfsByIDsRow
	fabrics     []dbq.SearchFabricsByIDsRow
	assetsMeta  []dbq.SearchAssetsByIDsRow

	gotSitesPattern  string
	gotRacksPattern  string
	gotAssetsPattern string
	gotIPHost        string
	gotIPLimit       int32
	gotSitesLimit    int32

	sitesErr error
	ipErr    error
}

func (f *fakeQ) SearchSites(_ context.Context, a dbq.SearchSitesParams) ([]dbq.SearchSitesRow, error) {
	f.gotSitesPattern = a.Pattern
	f.gotSitesLimit = a.Limit
	return f.sites, f.sitesErr
}
func (f *fakeQ) SearchRacks(_ context.Context, a dbq.SearchRacksParams) ([]dbq.SearchRacksRow, error) {
	f.gotRacksPattern = a.Pattern
	return f.racks, nil
}
func (f *fakeQ) SearchAssets(_ context.Context, a dbq.SearchAssetsParams) ([]dbq.SearchAssetsRow, error) {
	f.gotAssetsPattern = a.Pattern
	return f.assets, nil
}
func (f *fakeQ) SearchIPAddressesByHost(_ context.Context, a dbq.SearchIPAddressesByHostParams) ([]dbq.SearchIPAddressesByHostRow, error) {
	f.gotIPHost = a.Host
	f.gotIPLimit = a.Limit
	return f.ips, f.ipErr
}
func (f *fakeQ) SearchSubnetsByIDs(_ context.Context, _ []uuid.UUID) ([]dbq.SearchSubnetsByIDsRow, error) {
	return f.subnets, nil
}
func (f *fakeQ) SearchVrfsByIDs(_ context.Context, _ []uuid.UUID) ([]dbq.SearchVrfsByIDsRow, error) {
	return f.vrfs, nil
}
func (f *fakeQ) SearchFabricsByIDs(_ context.Context, _ []uuid.UUID) ([]dbq.SearchFabricsByIDsRow, error) {
	return f.fabrics, nil
}
func (f *fakeQ) SearchAssetsByIDs(_ context.Context, _ []uuid.UUID) ([]dbq.SearchAssetsByIDsRow, error) {
	return f.assetsMeta, nil
}

func mount(f *fakeQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}

func do(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	return authtest.ServeRequest(h, authtest.PrincipalWithCaps("search:search:read"), "GET", path, nil)
}

func TestGlobalSearch_AllBucketsRender(t *testing.T) {
	sid, rid, aid := uuid.New(), uuid.New(), uuid.New()
	f := &fakeQ{
		sites:  []dbq.SearchSitesRow{{ID: sid, Name: "Site A", Code: "SA"}},
		racks:  []dbq.SearchRacksRow{{ID: rid, Name: "Rack A", SiteID: sid}},
		assets: []dbq.SearchAssetsRow{{ID: aid, Name: "Asset A", Kind: "switch", SiteID: sid}},
	}
	rec := do(t, mount(f), "/search?q=a&limit=10")
	if rec.Code != http.StatusBadRequest {
		// q must be 2+; "a" → 400. Sanity check the boundary firing.
		t.Errorf("status = %d, want 400 for q=a", rec.Code)
	}
	rec = do(t, mount(f), "/search?q=ab&limit=10")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if f.gotSitesPattern != "%ab%" {
		t.Errorf("sites pattern = %q, want %%ab%%", f.gotSitesPattern)
	}
	if f.gotSitesLimit != 10 {
		t.Errorf("sites limit = %d, want 10", f.gotSitesLimit)
	}
	var body searchResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Query != "ab" {
		t.Errorf("query = %q, want ab", body.Query)
	}
	if body.ParsedIP != nil {
		t.Errorf("parsed_ip = %v, want nil", body.ParsedIP)
	}
	if len(body.Results.Sites) != 1 || body.Results.Sites[0].Code != "SA" {
		t.Errorf("sites bucket: %+v", body.Results.Sites)
	}
	if len(body.Results.Racks) != 1 || body.Results.Racks[0].SiteID != sid {
		t.Errorf("racks bucket: %+v", body.Results.Racks)
	}
	if len(body.Results.Assets) != 1 || body.Results.Assets[0].Kind != "switch" {
		t.Errorf("assets bucket: %+v", body.Results.Assets)
	}
	if body.Results.IPs == nil {
		t.Errorf("ips should be empty array, not null")
	}
}

func TestGlobalSearch_EmptyBucketsAreArrays(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/search?q=zz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	// Verify the JSON encodes `[]` for every bucket so finch's
	// `.map(...)` calls don't NPE on null.
	if !contains(rec.Body.String(), `"sites":[]`) || !contains(rec.Body.String(), `"racks":[]`) {
		t.Errorf("buckets should encode as [], got: %s", rec.Body.String())
	}
}

func TestGlobalSearch_QueryTooShort(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/search?q=x")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("q=1char should 400; got %d", rec.Code)
	}
}

func TestGlobalSearch_QueryMissing(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/search")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing q should 400; got %d", rec.Code)
	}
}

func TestGlobalSearch_RejectsWithoutCap(t *testing.T) {
	r := chi.NewRouter()
	(&Handler{Q: &fakeQ{}}).Mount(r)
	rec := authtest.ServeRequest(r, authtest.PrincipalWithCaps("inventory:sites:read"), "GET", "/search?q=ab", nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// IP-parse path: q looks like an IPv4 → SearchIPAddressesByHost
// receives the canonical form, the enrichment fan-out runs, and the
// ips bucket carries the joined fields.
func TestGlobalSearch_IPParsePath(t *testing.T) {
	ipID, sid, vid, fid, aid := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	subID := sid
	asPtr := aid
	f := &fakeQ{
		ips: []dbq.SearchIPAddressesByHostRow{
			{ID: ipID, SubnetID: sid, AssetID: &asPtr, Address: "10.0.0.5", Role: "host", Status: "active", Source: "manual"},
		},
		subnets: []dbq.SearchSubnetsByIDsRow{
			{ID: subID, FabricID: fid, VrfID: vid, Prefix: "10.0.0.0/24"},
		},
		vrfs:       []dbq.SearchVrfsByIDsRow{{ID: vid, Name: "default"}},
		fabrics:    []dbq.SearchFabricsByIDsRow{{ID: fid, Name: "Site A Fab"}},
		assetsMeta: []dbq.SearchAssetsByIDsRow{{ID: aid, Name: "switch-a"}},
	}
	rec := do(t, mount(f), "/search?q=10.0.0.5")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if f.gotIPHost != "10.0.0.5" {
		t.Errorf("ip host = %q, want 10.0.0.5", f.gotIPHost)
	}
	var body searchResponse
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.ParsedIP == nil || *body.ParsedIP != "10.0.0.5" {
		t.Errorf("parsed_ip = %v, want 10.0.0.5", body.ParsedIP)
	}
	if len(body.Results.IPs) != 1 {
		t.Fatalf("ips bucket should have one row: %+v", body.Results.IPs)
	}
	row := body.Results.IPs[0]
	if row.Address != "10.0.0.5" {
		t.Errorf("address = %q", row.Address)
	}
	if row.SubnetPrefix == nil || *row.SubnetPrefix != "10.0.0.0/24" {
		t.Errorf("subnet_prefix = %v", row.SubnetPrefix)
	}
	if row.VrfName == nil || *row.VrfName != "default" {
		t.Errorf("vrf_name = %v", row.VrfName)
	}
	if row.FabricName == nil || *row.FabricName != "Site A Fab" {
		t.Errorf("fabric_name = %v", row.FabricName)
	}
	if row.AssetName == nil || *row.AssetName != "switch-a" {
		t.Errorf("asset_name = %v", row.AssetName)
	}
}

// "10.0.0.5/24" → strip the /24, pass 10.0.0.5 to the IP query.
// Mirrors api/search.py::_looks_like_ip.
func TestGlobalSearch_IPParseStripsPrefix(t *testing.T) {
	f := &fakeQ{}
	rec := do(t, mount(f), "/search?q=10.0.0.5/24")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if f.gotIPHost != "10.0.0.5" {
		t.Errorf("expected stripped host 10.0.0.5, got %q", f.gotIPHost)
	}
}

func TestGlobalSearch_IPv6(t *testing.T) {
	f := &fakeQ{}
	rec := do(t, mount(f), "/search?q=2001:db8::1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if f.gotIPHost == "" {
		t.Errorf("v6 should trigger ip path")
	}
}

// Non-IP query → ParsedIP nil, IP path NOT invoked.
func TestGlobalSearch_NotAnIP(t *testing.T) {
	f := &fakeQ{}
	rec := do(t, mount(f), "/search?q=router-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if f.gotIPHost != "" {
		t.Errorf("ip path should not fire for %q; got host=%q", "router-1", f.gotIPHost)
	}
}

func TestGlobalSearch_LimitClamping(t *testing.T) {
	f := &fakeQ{}
	rec := do(t, mount(f), "/search?q=ab&limit=999")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if f.gotSitesLimit != 200 {
		t.Errorf("limit clamped to 200; got %d", f.gotSitesLimit)
	}

	f = &fakeQ{}
	rec = do(t, mount(f), "/search?q=ab&limit=-5")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if f.gotSitesLimit != 1 {
		t.Errorf("limit clamped to 1; got %d", f.gotSitesLimit)
	}

	f = &fakeQ{}
	rec = do(t, mount(f), "/search?q=ab")
	if f.gotSitesLimit != 25 {
		t.Errorf("default limit = 25; got %d", f.gotSitesLimit)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestCanonicalIP(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"10.0.0.5", "10.0.0.5"},
		{"10.0.0.5/24", "10.0.0.5"},
		{"  10.0.0.5  ", "10.0.0.5"},
		{"2001:db8::1", "2001:db8::1"},
		{"not-an-ip", ""},
		{"", ""},
		{"router-1", ""},
	}
	for _, c := range cases {
		if got := canonicalIP(c.in); got != c.want {
			t.Errorf("canonicalIP(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func contains(s, sub string) bool {
	return indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// silence unused-import on context.
var _ = context.Background
