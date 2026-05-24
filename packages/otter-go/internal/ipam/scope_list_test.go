package ipam

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

// PR 56: scope-filtered LIST queries. The handler should:
//   - pass nil ScopeFabricIds when the principal is global for the cap
//   - pass the principal's fabric set when it's fabric-scoped
//   - short-circuit to an empty page when fabric-scoped but the set is empty

func doListAuthed(t *testing.T, q *fakeQ, p auth.Principal, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	(&Handler{Q: q}).Mount(r)
	req := httptest.NewRequest("GET", path, nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestListVrfs_GlobalPrincipal_NoScopeFilter(t *testing.T) {
	q := &fakeQ{}
	p := auth.Principal{Capabilities: []string{"*"}}
	rec := doListAuthed(t, q, p, "/ipam/vrfs")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	if q.lastVrf.ScopeFabricIds != nil {
		t.Errorf("global principal should pass nil ScopeFabricIds, got %v", q.lastVrf.ScopeFabricIds)
	}
}

func TestListVrfs_ScopedPrincipal_PassesSlice(t *testing.T) {
	owned := uuid.New()
	q := &fakeQ{}
	p := auth.Principal{
		Capabilities: []string{"ipam:vrfs:read"},
		Scopes: map[string]auth.Scope{
			"ipam:vrfs:read": {FabricIDs: map[uuid.UUID]struct{}{owned: {}}},
		},
	}
	rec := doListAuthed(t, q, p, "/ipam/vrfs")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}
	if len(q.lastVrf.ScopeFabricIds) != 1 || q.lastVrf.ScopeFabricIds[0] != owned {
		t.Errorf("want ScopeFabricIds=[%s], got %v", owned, q.lastVrf.ScopeFabricIds)
	}
}

func TestListVrfs_ScopedEmpty_ShortCircuits(t *testing.T) {
	q := &fakeQ{}
	// Held cap but no fabric IDs (e.g. region-only scope).
	p := auth.Principal{
		Capabilities: []string{"ipam:vrfs:read"},
		Scopes: map[string]auth.Scope{
			"ipam:vrfs:read": {RegionIDs: map[uuid.UUID]struct{}{uuid.New(): {}}},
		},
	}
	rec := doListAuthed(t, q, p, "/ipam/vrfs")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"total":0`) {
		t.Errorf("expected empty page (total:0), got %q", rec.Body.String())
	}
	// fakeQ.lastVrf is the zero value because ListVrfs should never be called.
	// A non-nil ScopeFabricIds here would indicate the DB call wasn't skipped.
	if q.lastVrf.ScopeFabricIds != nil {
		t.Errorf("ListVrfs should not have been called; captured params=%+v", q.lastVrf)
	}
}

// Table-driven sanity check that the scope filter is wired through every
// LIST handler PR 56 touched. We verify the captured params on the fakeQ
// hold the correct ScopeFabricIds slice for a fabric-scoped principal.
func TestListEndpoints_ScopeFilterWiredThrough(t *testing.T) {
	owned := uuid.New()
	cases := []struct {
		name    string
		capCode string
		path    string
		got     func(*fakeQ) []uuid.UUID
	}{
		{"fabrics", "ipam:fabrics:read", "/ipam/fabrics", func(f *fakeQ) []uuid.UUID { return f.lastFabric.ScopeFabricIds }},
		{"vrfs", "ipam:vrfs:read", "/ipam/vrfs", func(f *fakeQ) []uuid.UUID { return f.lastVrf.ScopeFabricIds }},
		{"supernets", "ipam:supernets:read", "/ipam/supernets", func(f *fakeQ) []uuid.UUID { return f.lastSupernet.ScopeFabricIds }},
		{"subnets", "ipam:subnets:read", "/ipam/subnets", func(f *fakeQ) []uuid.UUID { return f.lastSubnet.ScopeFabricIds }},
		{"addresses", "ipam:addresses:read", "/ipam/addresses", func(f *fakeQ) []uuid.UUID { return f.lastAddr.ScopeFabricIds }},
		{"overlays", "ipam:overlays:read", "/ipam/overlays", func(f *fakeQ) []uuid.UUID { return f.lastOverlay.ScopeFabricIds }},
		{"vnis", "ipam:vnis:read", "/ipam/vnis", func(f *fakeQ) []uuid.UUID { return f.lastVni.ScopeFabricIds }},
		{"vteps", "ipam:vteps:read", "/ipam/vteps", func(f *fakeQ) []uuid.UUID { return f.lastVtep.ScopeFabricIds }},
		{"vtep-memberships", "ipam:vtep-memberships:read", "/ipam/vtep-memberships", func(f *fakeQ) []uuid.UUID { return f.lastMembership.ScopeFabricIds }},
		{"dhcp-servers", "ipam:dhcp-servers:read", "/ipam/dhcp/servers", func(f *fakeQ) []uuid.UUID { return f.lastDhcp.ScopeFabricIds }},
		{"vrf-bgp-peers", "ipam:vrf-bgp-peers:read", "/ipam/vrf-bgp-peers", func(f *fakeQ) []uuid.UUID { return f.lastVrfPeer.ScopeFabricIds }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &fakeQ{}
			p := auth.Principal{
				Capabilities: []string{tc.capCode},
				Scopes: map[string]auth.Scope{
					tc.capCode: {FabricIDs: map[uuid.UUID]struct{}{owned: {}}},
				},
			}
			rec := doListAuthed(t, q, p, tc.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("got %d, want 200 (body=%q)", rec.Code, rec.Body.String())
			}
			got := tc.got(q)
			if len(got) != 1 || got[0] != owned {
				t.Errorf("ScopeFabricIds: want [%s], got %v", owned, got)
			}
		})
	}
}
