package dns

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

// scopedFakeQ returns a fixed fabric_id from every PR 57 parent-lookup
// query so EnforceFabricScope has a real target to compare against.
type scopedFakeQ struct {
	fakeQ
	fabric uuid.UUID
}

func (s *scopedFakeQ) GetDnsZoneFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return s.fabric, nil
}
func (s *scopedFakeQ) GetDnsZone(_ context.Context, id uuid.UUID) (dbq.DnsZone, error) {
	return dbq.DnsZone{ID: id, FabricID: s.fabric, Frozen: false}, nil
}
func (s *scopedFakeQ) GetDnsRecordFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return s.fabric, nil
}
func (s *scopedFakeQ) GetDnsServerFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return s.fabric, nil
}
func (s *scopedFakeQ) GetAnycastGroupFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return s.fabric, nil
}
func (s *scopedFakeQ) GetDnsForwarderFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return s.fabric, nil
}
func (s *scopedFakeQ) GetDnsCatalogZoneFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return s.fabric, nil
}
func (s *scopedFakeQ) GetDnsBlocklistFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return s.fabric, nil
}
func (s *scopedFakeQ) GetDnsBlocklistEntryFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return s.fabric, nil
}
func (s *scopedFakeQ) GetDnsViewFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return s.fabric, nil
}
func (s *scopedFakeQ) GetDnsHealthCheckFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return s.fabric, nil
}

// deleteForbidden mounts a fabric-scoped principal against a fabric the
// scopedFakeQ pretends the target resource lives in, asserts 403.
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

func TestEnforceFabric_DnsDeletes_Forbidden(t *testing.T) {
	cases := []struct {
		name    string
		capCode string
		path    string
	}{
		{"zone", "dns:zones:delete", "/dns/zones/"},
		{"record", "dns:records:delete", "/dns/records/"},
		{"server", "dns:servers:delete", "/dns/servers/"},
		{"anycast-group", "dns:anycast-groups:delete", "/dns/anycast-groups/"},
		{"forwarder", "dns:forwarders:delete", "/dns/forwarders/"},
		{"catalog-zone", "dns:catalog-zones:delete", "/dns/catalog-zones/"},
		{"blocklist", "dns:blocklists:delete", "/dns/blocklists/"},
		{"view", "dns:views:delete", "/dns/views/"},
		{"health-check", "dns:health-checks:delete", "/dns/health-checks/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deleteForbidden(t, tc.capCode, tc.path)
		})
	}
}

func TestEnforceFabric_DnsZoneDelete_InScope(t *testing.T) {
	owned := uuid.New()
	q := &scopedFakeQ{fabric: owned}
	r := chi.NewRouter()
	(&Handler{Q: q}).Mount(r)

	p := auth.Principal{
		Capabilities: []string{"dns:zones:delete"},
		Scopes: map[string]auth.Scope{
			"dns:zones:delete": {FabricIDs: map[uuid.UUID]struct{}{owned: {}}},
		},
	}
	req := httptest.NewRequest("DELETE", "/dns/zones/"+uuid.New().String(), nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got %d, want 204 (body=%q)", rec.Code, rec.Body.String())
	}
}

// dns_records create resolves the fabric via the parent zone lookup;
// verify that path also gates on scope.
func TestEnforceFabric_DnsRecordCreate_Forbidden(t *testing.T) {
	owned := uuid.New()
	other := uuid.New()
	q := &scopedFakeQ{fabric: other}
	r := chi.NewRouter()
	(&Handler{Q: q}).Mount(r)

	p := auth.Principal{
		Capabilities: []string{"dns:records:create"},
		Scopes: map[string]auth.Scope{
			"dns:records:create": {FabricIDs: map[uuid.UUID]struct{}{owned: {}}},
		},
	}
	body := `{"zone_id":"` + uuid.New().String() + `","name":"foo","type":"A","data":["10.0.0.1"]}`
	req := httptest.NewRequest("POST", "/dns/records", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 (body=%q)", rec.Code, rec.Body.String())
	}
}

func TestEnforceFabric_DnsGlobalPrincipalUnaffected(t *testing.T) {
	q := &scopedFakeQ{fabric: uuid.New()}
	r := chi.NewRouter()
	(&Handler{Q: q}).Mount(r)
	// Wildcard cap → global scope per FindScope contract.
	p := auth.Principal{Capabilities: []string{"*"}}

	req := httptest.NewRequest("DELETE", "/dns/zones/"+uuid.New().String(), nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got %d, want 204 (body=%q)", rec.Code, rec.Body.String())
	}
}
