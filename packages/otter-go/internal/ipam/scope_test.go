package ipam

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

// scopedFakeQ returns a fixed fabric_id from the parent-lookup queries
// so EnforceFabricScope has a real target to compare against.
type scopedFakeQ struct {
	fakeQ
	fabric uuid.UUID
}

func (s *scopedFakeQ) GetVrfFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return s.fabric, nil
}
func (s *scopedFakeQ) GetVrf(_ context.Context, id uuid.UUID) (dbq.Vrf, error) {
	return dbq.Vrf{ID: id, FabricID: s.fabric, IsDefault: false}, nil
}

func (s *scopedFakeQ) GetOverlayFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return s.fabric, nil
}

// PR 55 2+ hop transitive lookups.
func (s *scopedFakeQ) GetSubnetFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return s.fabric, nil
}
func (s *scopedFakeQ) GetIPAddressFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return s.fabric, nil
}
func (s *scopedFakeQ) GetVniFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return s.fabric, nil
}
func (s *scopedFakeQ) GetVtepFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return s.fabric, nil
}
func (s *scopedFakeQ) GetVtepMembershipFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return s.fabric, nil
}

func TestEnforceFabric_DeleteVrf_Forbidden(t *testing.T) {
	owned := uuid.New()
	other := uuid.New()
	q := &scopedFakeQ{fabric: other}
	r := chi.NewRouter()
	(&Handler{Q: q}).Mount(r)
	var h http.Handler = r

	p := auth.Principal{
		Capabilities: []string{"ipam:vrfs:delete"},
		Scopes: map[string]auth.Scope{
			"ipam:vrfs:delete": {FabricIDs: map[uuid.UUID]struct{}{owned: {}}},
		},
	}

	req := httptest.NewRequest("DELETE", "/ipam/vrfs/"+uuid.New().String(), nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "outside your scope") {
		t.Errorf("body=%q", rec.Body.String())
	}
}

func TestEnforceFabric_DeleteVrf_InScope(t *testing.T) {
	owned := uuid.New()
	q := &scopedFakeQ{fabric: owned}
	r := chi.NewRouter()
	(&Handler{Q: q}).Mount(r)
	var h http.Handler = r

	p := auth.Principal{
		Capabilities: []string{"ipam:vrfs:delete"},
		Scopes: map[string]auth.Scope{
			"ipam:vrfs:delete": {FabricIDs: map[uuid.UUID]struct{}{owned: {}}},
		},
	}

	req := httptest.NewRequest("DELETE", "/ipam/vrfs/"+uuid.New().String(), nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got %d, want 204", rec.Code)
	}
}

func TestEnforceFabric_GlobalPrincipalUnaffected(t *testing.T) {
	q := &scopedFakeQ{fabric: uuid.New()}
	r := chi.NewRouter()
	(&Handler{Q: q}).Mount(r)
	var h http.Handler = r
	// No Scopes map → wildcard cap → global per FindScope contract.
	p := auth.Principal{Capabilities: []string{"*"}}

	req := httptest.NewRequest("DELETE", "/ipam/vrfs/"+uuid.New().String(), nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got %d, want 204", rec.Code)
	}
}

// ---- PR 55: 2+ hop transitive ABAC ----

// deleteForbidden mounts a fabric-scoped principal against a fabric the
// scopedFakeQ pretends the target resource lives in, asserts 403. Used
// by the table-driven test below to exercise every 2+ hop IPAM path.
func deleteForbidden(t *testing.T, capCode, path string) {
	t.Helper()
	owned := uuid.New()
	other := uuid.New()
	q := &scopedFakeQ{fabric: other}
	r := chi.NewRouter()
	(&Handler{Q: q}).Mount(r)

	p := auth.Principal{
		Capabilities: []string{capCode},
		Scopes: map[string]auth.Scope{
			capCode: {FabricIDs: map[uuid.UUID]struct{}{owned: {}}},
		},
	}
	req := httptest.NewRequest("DELETE", path+uuid.New().String(), nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("%s %s: got %d, want 403 (body=%q)", capCode, path, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "outside your scope") {
		t.Errorf("%s: body=%q", capCode, rec.Body.String())
	}
}

func TestEnforceFabric_TwoHopDeletes_Forbidden(t *testing.T) {
	cases := []struct {
		name    string
		capCode string
		path    string
	}{
		{"subnet", "ipam:subnets:delete", "/ipam/subnets/"},
		{"address", "ipam:addresses:delete", "/ipam/addresses/"},
		{"vni", "ipam:vnis:delete", "/ipam/vnis/"},
		{"vtep", "ipam:vteps:delete", "/ipam/vteps/"},
		{"vtep-membership", "ipam:vtep-memberships:delete", "/ipam/vtep-memberships/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deleteForbidden(t, tc.capCode, tc.path)
		})
	}
}

func TestEnforceFabric_DeleteSubnet_InScope(t *testing.T) {
	owned := uuid.New()
	q := &scopedFakeQ{fabric: owned}
	r := chi.NewRouter()
	(&Handler{Q: q}).Mount(r)

	p := auth.Principal{
		Capabilities: []string{"ipam:subnets:delete"},
		Scopes: map[string]auth.Scope{
			"ipam:subnets:delete": {FabricIDs: map[uuid.UUID]struct{}{owned: {}}},
		},
	}
	req := httptest.NewRequest("DELETE", "/ipam/subnets/"+uuid.New().String(), nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got %d, want 204 (body=%q)", rec.Code, rec.Body.String())
	}
}

// VNI/VTEP create resolves the fabric via overlay_id in the request
// body; verify that path also gates on scope.
func TestEnforceFabric_CreateVni_Forbidden(t *testing.T) {
	owned := uuid.New()
	other := uuid.New()
	q := &scopedFakeQ{fabric: other}
	r := chi.NewRouter()
	(&Handler{Q: q}).Mount(r)

	p := auth.Principal{
		Capabilities: []string{"ipam:vnis:create"},
		Scopes: map[string]auth.Scope{
			"ipam:vnis:create": {FabricIDs: map[uuid.UUID]struct{}{owned: {}}},
		},
	}
	body := `{"overlay_id":"` + uuid.New().String() + `","vni":42}`
	req := httptest.NewRequest("POST", "/ipam/vnis", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 (body=%q)", rec.Code, rec.Body.String())
	}
}
