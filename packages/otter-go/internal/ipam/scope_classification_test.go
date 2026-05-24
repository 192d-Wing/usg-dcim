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

// PR 61 — enclave + classification enforcement on fabric mutations.
// classifiedFabricFakeQ returns a Fabric tagged with a specific enclave
// + classification from GetFabric so the update/delete paths can read
// the current values.
type classifiedFabricFakeQ struct {
	fakeQ
	enclave        string
	classification string
}

func (q *classifiedFabricFakeQ) GetFabric(_ context.Context, id uuid.UUID) (dbq.Fabric, error) {
	enc := q.enclave
	cls := q.classification
	return dbq.Fabric{
		ID:             id,
		Enclave:        &enc,
		Classification: &cls,
	}, nil
}

// PR 54 enforces fabric scope on the id itself; that helper looks up
// nothing, so a scoped principal with the fabric in scope passes the
// fabric-id gate. The enclave/classification check on the current row
// is the new gate this PR adds.

func TestFabricCreate_EnclaveOutsideScope_403(t *testing.T) {
	q := &fakeQ{}
	r := chi.NewRouter()
	(&Handler{Q: q}).Mount(r)

	p := auth.Principal{
		Capabilities: []string{"ipam:fabrics:create"},
		Scopes: map[string]auth.Scope{
			"ipam:fabrics:create": {Enclaves: map[string]struct{}{"niprnet": {}}},
		},
	}
	body := `{"name":"f1","slug":"f1","enclave":"siprnet"}`
	req := httptest.NewRequest("POST", "/ipam/fabrics", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 (body=%q)", rec.Code, rec.Body.String())
	}
}

func TestFabricCreate_ClassificationOutsideScope_403(t *testing.T) {
	q := &fakeQ{}
	r := chi.NewRouter()
	(&Handler{Q: q}).Mount(r)

	p := auth.Principal{
		Capabilities: []string{"ipam:fabrics:create"},
		Scopes: map[string]auth.Scope{
			"ipam:fabrics:create": {Classifications: map[string]struct{}{"unclassified": {}}},
		},
	}
	body := `{"name":"f1","slug":"f1","classification":"secret"}`
	req := httptest.NewRequest("POST", "/ipam/fabrics", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 (body=%q)", rec.Code, rec.Body.String())
	}
}

func TestFabricUpdate_CurrentClassificationOutsideScope_403(t *testing.T) {
	owned := uuid.New()
	q := &classifiedFabricFakeQ{enclave: "niprnet", classification: "secret"}
	r := chi.NewRouter()
	(&Handler{Q: q}).Mount(r)

	// Principal owns the fabric_id and 'niprnet'/'unclassified' but the
	// fabric is currently tagged 'secret' — must be refused.
	p := auth.Principal{
		Capabilities: []string{"ipam:fabrics:update"},
		Scopes: map[string]auth.Scope{
			"ipam:fabrics:update": {
				FabricIDs:       map[uuid.UUID]struct{}{owned: {}},
				Enclaves:        map[string]struct{}{"niprnet": {}},
				Classifications: map[string]struct{}{"unclassified": {}},
			},
		},
	}
	req := httptest.NewRequest("PATCH", "/ipam/fabrics/"+owned.String(), strings.NewReader(`{"name":"f1-new"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 (body=%q)", rec.Code, rec.Body.String())
	}
}

func TestFabricUpdate_ReassignToOutOfScopeClassification_403(t *testing.T) {
	owned := uuid.New()
	q := &classifiedFabricFakeQ{enclave: "niprnet", classification: "unclassified"}
	r := chi.NewRouter()
	(&Handler{Q: q}).Mount(r)

	p := auth.Principal{
		Capabilities: []string{"ipam:fabrics:update"},
		Scopes: map[string]auth.Scope{
			"ipam:fabrics:update": {
				FabricIDs:       map[uuid.UUID]struct{}{owned: {}},
				Enclaves:        map[string]struct{}{"niprnet": {}},
				Classifications: map[string]struct{}{"unclassified": {}},
			},
		},
	}
	// Caller owns the current fabric + tags, but is trying to relabel
	// it 'secret' which they don't own — must be refused.
	body := `{"classification":"secret"}`
	req := httptest.NewRequest("PATCH", "/ipam/fabrics/"+owned.String(), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 (body=%q)", rec.Code, rec.Body.String())
	}
}

func TestFabricDelete_ClassificationOutsideScope_403(t *testing.T) {
	owned := uuid.New()
	q := &classifiedFabricFakeQ{enclave: "niprnet", classification: "secret"}
	r := chi.NewRouter()
	(&Handler{Q: q}).Mount(r)

	p := auth.Principal{
		Capabilities: []string{"ipam:fabrics:delete"},
		Scopes: map[string]auth.Scope{
			"ipam:fabrics:delete": {
				FabricIDs:       map[uuid.UUID]struct{}{owned: {}},
				Enclaves:        map[string]struct{}{"niprnet": {}},
				Classifications: map[string]struct{}{"unclassified": {}},
			},
		},
	}
	req := httptest.NewRequest("DELETE", "/ipam/fabrics/"+owned.String(), nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 (body=%q)", rec.Code, rec.Body.String())
	}
}

// Global principal sails through all four paths regardless of tags.
func TestFabricMutations_GlobalPrincipalUnaffected(t *testing.T) {
	q := &classifiedFabricFakeQ{enclave: "siprnet", classification: "secret"}
	r := chi.NewRouter()
	(&Handler{Q: q}).Mount(r)

	p := auth.Principal{Capabilities: []string{"*"}}

	// create
	body := `{"name":"f1","slug":"f1","enclave":"siprnet","classification":"secret"}`
	req := httptest.NewRequest("POST", "/ipam/fabrics", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Errorf("global create: got %d, want 201 (body=%q)", rec.Code, rec.Body.String())
	}
}
