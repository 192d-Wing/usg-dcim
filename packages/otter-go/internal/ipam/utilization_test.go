// PR 63 — unit + handler tests for supernet utilization. Pure
// networkCapacity tests pin the CIDR-arithmetic edge cases the
// Python services.ipam.network_capacity codifies (point-to-point
// /31 /127, host /32 /128, normal subtract-two, int64 clamp on
// wide v6). Handler test pins the response shape + the empty-
// subnets path through the supernet GET.
package ipam

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

func TestNetworkCapacity(t *testing.T) {
	cases := []struct {
		prefix string
		want   int64
	}{
		// v4
		{"10.0.0.0/24", 254},
		{"10.0.0.0/30", 2},
		{"10.0.0.0/31", 2},  // PtP — both addresses
		{"10.0.0.5/32", 1},  // host
		{"0.0.0.0/0", int64(1)<<32 - 2},
		// v6
		{"2001:db8::/126", 2},
		{"2001:db8::/127", 2}, // PtP
		{"2001:db8::1/128", 1},
		{"2001:db8::/64", int64Max}, // 2^64 clamps to int64Max
	}
	for _, c := range cases {
		got, err := networkCapacity(c.prefix)
		if err != nil {
			t.Errorf("networkCapacity(%q) unexpected error: %v", c.prefix, err)
			continue
		}
		if got != c.want {
			t.Errorf("networkCapacity(%q) = %d, want %d", c.prefix, got, c.want)
		}
	}
}

func TestNetworkCapacityRejectsBadInput(t *testing.T) {
	if _, err := networkCapacity("not-a-prefix"); err == nil {
		t.Error("expected error for malformed prefix")
	}
	if _, err := networkCapacity(""); err == nil {
		t.Error("expected error for empty prefix")
	}
}

// fakeUtilQ exists alongside the broader fakeQ so utilization tests
// can stub specific GetSupernet / ListSubnetPrefixesBySupernet /
// GetSubnet / ListAddressStringsInSubnet outcomes without
// disturbing the fakeQ defaults the rest of the handler suite
// relies on.
type fakeUtilQ struct {
	fakeQ
	supernet    dbq.GetSupernetRow
	supernetErr error
	prefixes    []string
	prefixesErr error
	subnet      dbq.GetSubnetRow
	subnetErr   error
	addresses   []string
	addressErr  error
	gotSupernet uuid.UUID
	gotPrefixes uuid.UUID
	gotSubnet   uuid.UUID
	gotAddrs    uuid.UUID
}

func (f *fakeUtilQ) GetSupernet(_ context.Context, id uuid.UUID) (dbq.GetSupernetRow, error) {
	f.gotSupernet = id
	return f.supernet, f.supernetErr
}

func (f *fakeUtilQ) ListSubnetPrefixesBySupernet(_ context.Context, id uuid.UUID) ([]string, error) {
	f.gotPrefixes = id
	return f.prefixes, f.prefixesErr
}

func (f *fakeUtilQ) GetSubnet(_ context.Context, id uuid.UUID) (dbq.GetSubnetRow, error) {
	f.gotSubnet = id
	return f.subnet, f.subnetErr
}

func (f *fakeUtilQ) ListAddressStringsInSubnet(_ context.Context, id uuid.UUID) ([]string, error) {
	f.gotAddrs = id
	return f.addresses, f.addressErr
}

func mountUtil(f *fakeUtilQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}

func TestSupernetUtilization_HappyPath(t *testing.T) {
	id := uuid.New()
	f := &fakeUtilQ{
		supernet: dbq.GetSupernetRow{ID: id, Prefix: "10.0.0.0/16"},
		prefixes: []string{"10.0.0.0/24", "10.0.1.0/24"}, // 254 + 254 = 508
	}
	srv := httptest.NewServer(mountUtil(f))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/ipam/supernets/" + id.String() + "/utilization")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out supernetUtilization
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	// /16 capacity = 2^16 - 2 = 65534
	if out.Capacity != 65534 {
		t.Errorf("Capacity = %d, want 65534", out.Capacity)
	}
	if out.AllocatedSubnetAddresses != 508 {
		t.Errorf("Allocated = %d, want 508", out.AllocatedSubnetAddresses)
	}
	if out.Free != 65534-508 {
		t.Errorf("Free = %d, want %d", out.Free, 65534-508)
	}
	if out.SubnetCount != 2 {
		t.Errorf("SubnetCount = %d, want 2", out.SubnetCount)
	}
	// Two-decimal percent: 508/65534 * 100 = 0.7752... → 0.78
	if out.Percent != 0.78 {
		t.Errorf("Percent = %v, want 0.78", out.Percent)
	}
}

func TestSupernetUtilization_NotFound(t *testing.T) {
	f := &fakeUtilQ{supernetErr: pgx.ErrNoRows}
	srv := httptest.NewServer(mountUtil(f))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/ipam/supernets/" + uuid.New().String() + "/utilization")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestSupernetUtilization_BadUUID(t *testing.T) {
	srv := httptest.NewServer(mountUtil(&fakeUtilQ{}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/ipam/supernets/not-a-uuid/utilization")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestSupernetUtilization_EmptySubnets(t *testing.T) {
	// A supernet with no child subnets — allocated=0, free=capacity,
	// percent=0. Pinning so the no-divide-by-zero guard stays in
	// place.
	id := uuid.New()
	f := &fakeUtilQ{
		supernet: dbq.GetSupernetRow{ID: id, Prefix: "10.0.0.0/24"},
		prefixes: []string{},
	}
	srv := httptest.NewServer(mountUtil(f))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/ipam/supernets/" + id.String() + "/utilization")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out supernetUtilization
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Capacity != 254 || out.AllocatedSubnetAddresses != 0 ||
		out.Free != 254 || out.SubnetCount != 0 || out.Percent != 0.0 {
		t.Errorf("empty-supernet response: %+v", out)
	}
}

func TestSupernetUtilization_SkipsUnparseableChildPrefixes(t *testing.T) {
	// A bad row in the subnets table shouldn't tank the whole
	// utilization read — the LIST endpoint surfaces the bad row
	// separately. This pins the "skip and continue" behavior so
	// nobody quietly flips it to fail-the-request later.
	id := uuid.New()
	f := &fakeUtilQ{
		supernet: dbq.GetSupernetRow{ID: id, Prefix: "10.0.0.0/16"},
		prefixes: []string{"10.0.0.0/24", "garbage", "10.0.1.0/24"},
	}
	srv := httptest.NewServer(mountUtil(f))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/ipam/supernets/" + id.String() + "/utilization")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 despite a bad child prefix", resp.StatusCode)
	}
	var out supernetUtilization
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.AllocatedSubnetAddresses != 508 {
		t.Errorf("Allocated = %d, want 508 (bad row skipped)", out.AllocatedSubnetAddresses)
	}
}

func TestSupernetUtilization_DBError(t *testing.T) {
	// Non-NoRows DB error from GetSupernet should map to 5xx, not 404.
	f := &fakeUtilQ{supernetErr: errors.New("connection refused")}
	srv := httptest.NewServer(mountUtil(f))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/ipam/supernets/" + uuid.New().String() + "/utilization")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 500 {
		t.Errorf("status = %d, want 5xx for DB error", resp.StatusCode)
	}
}

// ---- PR 64: subnet utilization ----

func TestNextFreeAddress(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		used   []string
		want   *string
	}{
		{
			name:   "first host in empty /24",
			prefix: "10.0.0.0/24",
			used:   nil,
			want:   strPtr("10.0.0.1"),
		},
		{
			name:   "skips network address",
			prefix: "10.0.0.0/24",
			used:   []string{"10.0.0.1", "10.0.0.2"},
			want:   strPtr("10.0.0.3"),
		},
		{
			name:   "ptp /31 hands back the first address",
			prefix: "10.0.0.0/31",
			used:   nil,
			want:   strPtr("10.0.0.0"),
		},
		{
			name:   "ptp /31 second address when first is used",
			prefix: "10.0.0.0/31",
			used:   []string{"10.0.0.0"},
			want:   strPtr("10.0.0.1"),
		},
		{
			name:   "host /32 yields the lone address",
			prefix: "10.0.0.5/32",
			used:   nil,
			want:   strPtr("10.0.0.5"),
		},
		{
			name:   "host /32 returns nil when used",
			prefix: "10.0.0.5/32",
			used:   []string{"10.0.0.5"},
			want:   nil,
		},
		{
			name:   "v6 ptp /127",
			prefix: "2001:db8::/127",
			used:   []string{"2001:db8::"},
			want:   strPtr("2001:db8::1"),
		},
		{
			name:   "ignores out-of-prefix entries in used",
			prefix: "10.0.0.0/30",
			used:   []string{"192.168.1.1", "10.0.0.1"},
			want:   strPtr("10.0.0.2"),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := nextFreeAddress(c.prefix, c.used)
			if (got == nil) != (c.want == nil) {
				t.Fatalf("nil-mismatch: got=%v want=%v", got, c.want)
			}
			if got != nil && *got != *c.want {
				t.Errorf("got %q, want %q", *got, *c.want)
			}
		})
	}
}

func TestSubnetUtilization_HappyPath(t *testing.T) {
	id := uuid.New()
	f := &fakeUtilQ{
		subnet:    dbq.GetSubnetRow{ID: id, Prefix: "10.0.0.0/24"},
		addresses: []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"},
	}
	srv := httptest.NewServer(mountUtil(f))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/ipam/subnets/" + id.String() + "/utilization")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out subnetUtilization
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Capacity != 254 {
		t.Errorf("Capacity = %d, want 254", out.Capacity)
	}
	if out.Allocated != 3 {
		t.Errorf("Allocated = %d, want 3", out.Allocated)
	}
	if out.Free != 251 {
		t.Errorf("Free = %d, want 251", out.Free)
	}
	// 3/254*100 = 1.1811... → 1.18
	if out.Percent != 1.18 {
		t.Errorf("Percent = %v, want 1.18", out.Percent)
	}
	if out.NextAvailable == nil || *out.NextAvailable != "10.0.0.4" {
		t.Errorf("NextAvailable = %v, want 10.0.0.4", out.NextAvailable)
	}
}

func TestSubnetUtilization_NotFound(t *testing.T) {
	f := &fakeUtilQ{subnetErr: pgx.ErrNoRows}
	srv := httptest.NewServer(mountUtil(f))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/ipam/subnets/" + uuid.New().String() + "/utilization")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestSubnetUtilization_BadUUID(t *testing.T) {
	srv := httptest.NewServer(mountUtil(&fakeUtilQ{}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/ipam/subnets/not-a-uuid/utilization")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestSubnetUtilization_EmptySubnet(t *testing.T) {
	// No addresses allocated yet — next_available should be the
	// first host, allocated=0, free=capacity.
	id := uuid.New()
	f := &fakeUtilQ{
		subnet:    dbq.GetSubnetRow{ID: id, Prefix: "10.0.0.0/24"},
		addresses: []string{},
	}
	srv := httptest.NewServer(mountUtil(f))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/ipam/subnets/" + id.String() + "/utilization")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out subnetUtilization
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Allocated != 0 || out.Free != 254 || out.Percent != 0.0 {
		t.Errorf("empty-subnet response: %+v", out)
	}
	if out.NextAvailable == nil || *out.NextAvailable != "10.0.0.1" {
		t.Errorf("NextAvailable = %v, want 10.0.0.1", out.NextAvailable)
	}
}

func TestSubnetUtilization_FullSubnet(t *testing.T) {
	// /30 has 2 host addresses. Filling both should yield
	// next_available=null, percent=100, free=0.
	id := uuid.New()
	f := &fakeUtilQ{
		subnet:    dbq.GetSubnetRow{ID: id, Prefix: "10.0.0.0/30"},
		addresses: []string{"10.0.0.1", "10.0.0.2"},
	}
	srv := httptest.NewServer(mountUtil(f))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/ipam/subnets/" + id.String() + "/utilization")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out subnetUtilization
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Free != 0 || out.Percent != 100.0 {
		t.Errorf("full-subnet response: %+v", out)
	}
	if out.NextAvailable != nil {
		t.Errorf("NextAvailable = %v, want nil", *out.NextAvailable)
	}
}
