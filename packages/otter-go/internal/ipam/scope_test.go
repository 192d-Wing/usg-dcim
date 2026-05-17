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
