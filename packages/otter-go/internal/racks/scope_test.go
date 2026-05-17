package racks

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

type scopedFakeQ struct {
	fakeQ
	siteID uuid.UUID
}

func (s *scopedFakeQ) GetRack(_ context.Context, id uuid.UUID) (dbq.Rack, error) {
	return dbq.Rack{ID: id, SiteID: s.siteID, UHeight: 42}, nil
}

func TestEnforceSite_CreateRack_Forbidden(t *testing.T) {
	owned := uuid.New()
	other := uuid.New()
	q := &scopedFakeQ{siteID: other}
	r := chi.NewRouter()
	(&Handler{Q: q}).Mount(r)

	p := auth.Principal{
		Capabilities: []string{"inventory:racks:create"},
		Scopes: map[string]auth.Scope{
			"inventory:racks:create": {SiteIDs: map[uuid.UUID]struct{}{owned: {}}},
		},
	}
	body := []byte(`{"site_id":"` + other.String() + `","row_id":"` + uuid.New().String() + `","name":"r1","code":"R1"}`)
	req := httptest.NewRequest("POST", "/racks", bytes.NewReader(body))
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", rec.Code)
	}
}

func TestEnforceSite_UpdateRack_InScope(t *testing.T) {
	owned := uuid.New()
	q := &scopedFakeQ{siteID: owned}
	r := chi.NewRouter()
	(&Handler{Q: q}).Mount(r)

	p := auth.Principal{
		Capabilities: []string{"inventory:racks:update"},
		Scopes: map[string]auth.Scope{
			"inventory:racks:update": {SiteIDs: map[uuid.UUID]struct{}{owned: {}}},
		},
	}
	req := httptest.NewRequest("PATCH", "/racks/"+uuid.New().String(), bytes.NewReader([]byte(`{"name":"updated"}`)))
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
}
