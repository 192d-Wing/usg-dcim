package sites

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

// PR 62 — sites LIST scope filtering.

func TestSitesList_GlobalPrincipal_NoExpansion(t *testing.T) {
	called := false
	f := &fakeQuerier{
		list: func(_ context.Context, arg dbq.ListSitesParams) ([]dbq.Site, error) {
			if arg.SiteIDs != nil {
				t.Errorf("global principal should pass nil SiteIDs, got %v", arg.SiteIDs)
			}
			return nil, nil
		},
		count: func(_ context.Context, arg dbq.CountSitesParams) (int64, error) {
			if arg.SiteIDs != nil {
				t.Errorf("global principal should pass nil SiteIDs to count, got %v", arg.SiteIDs)
			}
			return 0, nil
		},
		expandSite: func(_ context.Context, _ dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error) {
			called = true
			return nil, nil
		},
	}
	p := auth.Principal{Capabilities: []string{"*"}}
	req := httptest.NewRequest("GET", "/sites", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mount(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	if called {
		t.Errorf("expansion query should not run for global principal")
	}
}

func TestSitesList_ScopedPrincipal_PassesExpandedIds(t *testing.T) {
	owned := uuid.New()
	other := uuid.New()
	gotListArg := dbq.ListSitesParams{}
	gotCountArg := dbq.CountSitesParams{}
	f := &fakeQuerier{
		list: func(_ context.Context, arg dbq.ListSitesParams) ([]dbq.Site, error) {
			gotListArg = arg
			return []dbq.Site{{ID: owned}, {ID: other}}, nil
		},
		count: func(_ context.Context, arg dbq.CountSitesParams) (int64, error) {
			gotCountArg = arg
			return 2, nil
		},
		expandSite: func(_ context.Context, _ dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error) {
			return []uuid.UUID{owned, other}, nil
		},
	}
	p := auth.Principal{
		Capabilities: []string{"inventory:sites:read"},
		Scopes: map[string]auth.Scope{
			"inventory:sites:read": {SiteIDs: map[uuid.UUID]struct{}{owned: {}, other: {}}},
		},
	}
	req := httptest.NewRequest("GET", "/sites", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mount(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}
	if len(gotListArg.SiteIDs) != 2 {
		t.Errorf("ListSites SiteIDs should be 2 ids, got %v", gotListArg.SiteIDs)
	}
	if len(gotCountArg.SiteIDs) != 2 {
		t.Errorf("CountSites SiteIDs should be 2 ids, got %v", gotCountArg.SiteIDs)
	}
}

func TestSitesList_ScopedEmpty_ShortCircuits(t *testing.T) {
	// Principal is scoped (cap held) but has no site-reachable
	// dimensions (e.g. fabric-only). Expansion returns empty set,
	// handler returns empty page without calling ListSites.
	listCalled := false
	f := &fakeQuerier{
		list: func(context.Context, dbq.ListSitesParams) ([]dbq.Site, error) {
			listCalled = true
			return nil, nil
		},
		expandSite: func(_ context.Context, _ dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error) {
			return nil, nil // mimics ListSiteIDsForExpansion called with all-NULL inputs
		},
	}
	p := auth.Principal{
		Capabilities: []string{"inventory:sites:read"},
		Scopes: map[string]auth.Scope{
			"inventory:sites:read": {FabricIDs: map[uuid.UUID]struct{}{uuid.New(): {}}},
		},
	}
	req := httptest.NewRequest("GET", "/sites", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mount(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}
	var body listResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 0 {
		t.Errorf("expected total=0, got %d", body.Total)
	}
	if listCalled {
		t.Errorf("ListSites should not be called when scope expands to empty set")
	}
}
