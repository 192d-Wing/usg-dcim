package sites

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

// PR 61 — site create rejects callers tagging the site with an enclave
// or classification they don't own.

func mountWith(q *fakeQuerier) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: q}).Mount(r)
	return r
}

func TestSiteCreate_EnclaveOutsideScope_403(t *testing.T) {
	q := &fakeQuerier{}
	h := mountWith(q)

	p := auth.Principal{
		Capabilities: []string{"inventory:sites:create"},
		Scopes: map[string]auth.Scope{
			"inventory:sites:create": {Enclaves: map[string]struct{}{"niprnet": {}}},
		},
	}
	body := `{"region_id":"` + uuid.New().String() + `","name":"S1","code":"AAA","enclave":"siprnet"}`
	req := httptest.NewRequest("POST", "/sites", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 (body=%q)", rec.Code, rec.Body.String())
	}
}

func TestSiteCreate_ClassificationOutsideScope_403(t *testing.T) {
	q := &fakeQuerier{}
	h := mountWith(q)

	p := auth.Principal{
		Capabilities: []string{"inventory:sites:create"},
		Scopes: map[string]auth.Scope{
			"inventory:sites:create": {Classifications: map[string]struct{}{"unclassified": {}}},
		},
	}
	body := `{"region_id":"` + uuid.New().String() + `","name":"S1","code":"AAA","classification":"secret"}`
	req := httptest.NewRequest("POST", "/sites", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 (body=%q)", rec.Code, rec.Body.String())
	}
}

// Scoped principal with no enclave dim trying to create an unlabeled
// site (no enclave field) — must be refused per "unlabeled = global only".
func TestSiteCreate_NilEnclave_ScopedRefused(t *testing.T) {
	q := &fakeQuerier{}
	h := mountWith(q)

	p := auth.Principal{
		Capabilities: []string{"inventory:sites:create"},
		Scopes: map[string]auth.Scope{
			"inventory:sites:create": {Enclaves: map[string]struct{}{"niprnet": {}}},
		},
	}
	// No enclave / classification in body.
	body := `{"region_id":"` + uuid.New().String() + `","name":"S1","code":"AAA"}`
	req := httptest.NewRequest("POST", "/sites", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 (body=%q)", rec.Code, rec.Body.String())
	}
}

func TestSiteCreate_GlobalPrincipalAllowsAnyTag(t *testing.T) {
	q := &fakeQuerier{}
	h := mountWith(q)

	p := auth.Principal{Capabilities: []string{"*"}}
	body := `{"region_id":"` + uuid.New().String() + `","name":"S1","code":"AAA","enclave":"siprnet","classification":"secret"}`
	req := httptest.NewRequest("POST", "/sites", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body=%q)", rec.Code, rec.Body.String())
	}
}

func TestSiteCreate_ScopedAndInScope_OK(t *testing.T) {
	q := &fakeQuerier{}
	h := mountWith(q)

	p := auth.Principal{
		Capabilities: []string{"inventory:sites:create"},
		Scopes: map[string]auth.Scope{
			"inventory:sites:create": {
				Enclaves:        map[string]struct{}{"niprnet": {}},
				Classifications: map[string]struct{}{"unclassified": {}},
			},
		},
	}
	body := `{"region_id":"` + uuid.New().String() + `","name":"S1","code":"AAA","enclave":"niprnet","classification":"unclassified"}`
	req := httptest.NewRequest("POST", "/sites", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body=%q)", rec.Code, rec.Body.String())
	}
}
